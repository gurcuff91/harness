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

// Ensure Anthropic implements Provider at compile time.
var _ Provider = (*Anthropic)(nil)

// Anthropic implements Provider for the Anthropic Messages API.
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
	a := &Anthropic{client: &http.Client{}, cache: make(map[string]types.ModelMeta)}
	// ResolveCredentials populates a.apiKey from env or file
	a.ResolveCredentials() //nolint:errcheck
	return a
}

func (a *Anthropic) Name() string        { return "anthropic" }
func (a *Anthropic) DisplayName() string { return "Anthropic" }
func (a *Anthropic) Description() string { return describeState(a) }
func (a *Anthropic) ActivationSource() ActivationSource {
	if v := os.Getenv(anthropicAPIKeyEnv); v != "" {
		return ActivationEnvVar
	}
	if v, ok := config.GetCredentialsManager().Load(anthropicAPIKeyCred); ok && v != "" {
		return ActivationCredentials
	}
	return ActivationNone
}
func (a *Anthropic) IsActive() bool {
	_, err := a.ResolveCredentials()
	return err == nil
}

func (a *Anthropic) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (a *Anthropic) ResolveCredentials() (types.Credentials, error) {
	if a.apiKey != "" {
		return types.APIKeyCredentials(a.apiKey), nil
	}
	if v := os.Getenv(anthropicAPIKeyEnv); v != "" {
		a.apiKey = v
		return types.APIKeyCredentials(v), nil
	}
	if v, ok := config.GetCredentialsManager().Load(anthropicAPIKeyCred); ok && v != "" {
		a.apiKey = v
		return types.APIKeyCredentials(v), nil
	}
	return types.Credentials{}, fmt.Errorf("no credentials found")
}

func (a *Anthropic) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("anthropic expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}

	// Validate first (in-memory only, no disk write)
	a.apiKey = creds.APIKey
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta)
	a.mu.Unlock()
	if _, err := a.FetchModels(); err != nil {
		a.apiKey = "" // clear in-memory
		return fmt.Errorf("invalid credentials: %w", err)
	}

	// Persist to disk only after validation succeeded
	return config.GetCredentialsManager().Store(anthropicAPIKeyCred, creds.APIKey)
}

func (a *Anthropic) Disconnect() error {
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta)
	a.mu.Unlock()
	a.apiKey = ""
	return config.GetCredentialsManager().Delete(anthropicAPIKeyCred)
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

func (a *Anthropic) FetchModels() ([]types.ModelMeta, error) {
	metas, err := fetchAnthropicModels(a.apiKey)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		a.cache[m.ID] = m
	}
	a.mu.Unlock()
	return metas, nil
}

func (a *Anthropic) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	meta := a.ModelMeta(req.Model)
	thinkingFull := llm.BuildAnthropicThinkingFromMeta(meta, req.ThinkingLevel, req.MaxTokens)

	extraHeaders := map[string]string{}
	// Interleaved-thinking beta only for non-adaptive models
	if req.ThinkingLevel != "" && req.ThinkingLevel != "off" && thinkingFull.OutputConfig == nil {
		extraHeaders["anthropic-beta"] = "interleaved-thinking-2025-05-14"
	}

	anthrReq := &llm.AnthropicRequest{
		Request:        req,
		ThinkingConfig: &thinkingFull,
	}
	return llm.DoAnthropicStream(ctx, a.client, anthropicAPI, a.apiKey, anthrReq, extraHeaders, cb)
}

const anthropicAPI = "https://api.anthropic.com/v1/messages"

func fetchAnthropicModels(tokenOrKey string) ([]types.ModelMeta, error) {
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
			return nil, fmt.Errorf("provider unreachable")
		}
	}
	if resp == nil || resp.StatusCode == 401 || resp.StatusCode == 403 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("API error (status %d)", resp.StatusCode)
	}
	defer resp.Body.Close()

	// Use a flexible structure to capture the nested capabilities
	var result struct {
		Data []struct {
			ID              string         `json:"id"`
			DisplayName     string         `json:"display_name"`
			MaxInputTokens  int            `json:"max_input_tokens"`
			MaxOutputTokens int            `json:"max_tokens"`
			Capabilities    map[string]any `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse models response")
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
		}

		// Parse capabilities
		if m.Capabilities != nil {
			// Vision
			if img, ok := m.Capabilities["image_input"].(map[string]any); ok {
				meta.Vision, _ = img["supported"].(bool)
			}
			// Thinking capabilities
			if t, ok := m.Capabilities["thinking"].(map[string]any); ok {
				meta.Thinking, _ = t["supported"].(bool)
				if types2, ok := t["types"].(map[string]any); ok {
					if adaptive, ok := types2["adaptive"].(map[string]any); ok {
						meta.ThinkingAdaptive, _ = adaptive["supported"].(bool)
					}
					if enabled, ok := types2["enabled"].(map[string]any); ok {
						meta.ThinkingLegacy, _ = enabled["supported"].(bool)
					}
				}
			}
		}

		llm.ApplyRegistryPricing(&meta)
		metas = append(metas, meta)
	}
	return metas, nil
}
