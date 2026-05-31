package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// Anthropic implements llm.Provider for the Anthropic Messages API.
type Anthropic struct {
	client *http.Client
	apiKey string
	cache  map[string]types.ModelMeta
	mu     sync.RWMutex
}

const (
	anthropicAPIKeyCred = "anthropic.api_key"
	anthropicAPIKeyEnv  = "ANTHROPIC_API_KEY"
)

func NewAnthropic() *Anthropic {
	apiKey := os.Getenv(anthropicAPIKeyEnv)
	if apiKey == "" {
		apiKey, _ = config.LoadCred(anthropicAPIKeyCred)
	}
	a := &Anthropic{client: &http.Client{}, cache: make(map[string]types.ModelMeta), apiKey: apiKey}
	if a.IsActive() {
		a.FetchModels()
	}
	return a
}

func (a *Anthropic) Name() string   { return "anthropic" }
func (a *Anthropic) IsActive() bool { return a.apiKey != "" }

func (a *Anthropic) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (a *Anthropic) SetCredentials(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("anthropic expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}
	a.apiKey = creds.APIKey
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta)
	a.mu.Unlock()
	config.StoreCred(anthropicAPIKeyCred, creds.APIKey)
	a.FetchModels()
	return nil
}

func (a *Anthropic) ClearCredentials() error {
	a.apiKey = ""
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta)
	a.mu.Unlock()
	return config.DeleteCred(anthropicAPIKeyCred)
}

func (a *Anthropic) Models() []types.ModelMeta {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(a.cache))
	for _, m := range a.cache {
		out = append(out, m)
	}
	return out
}

func (a *Anthropic) ModelMeta(modelID string) *types.ModelMeta {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if m, ok := a.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (a *Anthropic) FetchModels() []types.ModelMeta {
	metas := fetchAnthropicModels(a.apiKey)
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		a.cache[m.ID] = m
	}
	a.mu.Unlock()
	return metas
}

func (a *Anthropic) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoAnthropicStream(ctx, a.client, anthropicAPI,
		a.apiKey, req,
		map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14"},
		nil, cb)
}

func (a *Anthropic) FormatUserMessage(text string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"role": "user", "content": text})
	return data
}

func (a *Anthropic) FormatUserMessageWithImages(text string, images []types.ImageData) json.RawMessage {
	var content []map[string]any
	for _, img := range images {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]string{
				"type": "base64", "media_type": img.MimeType, "data": img.Base64,
			},
		})
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	data, _ := json.Marshal(map[string]any{"role": "user", "content": content})
	return data
}

const anthropicAPI = "https://api.anthropic.com/v1/messages"

func fetchAnthropicModels(tokenOrKey string) []types.ModelMeta {
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("x-api-key", tokenOrKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+tokenOrKey)
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		req.Header.Set("x-app", "cli")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID              string `json:"id"`
			DisplayName     string `json:"display_name"`
			MaxInputTokens  int    `json:"max_input_tokens"`
			MaxOutputTokens int    `json:"max_tokens"`
			Capabilities    struct {
				Vision   bool `json:"vision"`
				Thinking bool `json:"extended_thinking"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var metas []types.ModelMeta
	for _, m := range result.Data {
		if len(m.ID) == 0 || m.ID[0] != 'c' {
			continue
		}
		cw := m.MaxInputTokens
		if cw <= 0 {
			cw = 200000
		}
		mt := m.MaxOutputTokens
		if mt <= 0 {
			mt = 64000
		}
		meta := types.ModelMeta{
			ID: m.ID, DisplayName: m.DisplayName,
			ContextWindow: cw, MaxTokens: mt,
			Vision: m.Capabilities.Vision, Thinking: m.Capabilities.Thinking,
		}
		llm.ApplyRegistryPricing(&meta)
		metas = append(metas, meta)
	}
	return metas
}
