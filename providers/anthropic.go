package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
)

// Anthropic implements llm.Provider for the Anthropic Messages API.
type Anthropic struct {
	client       *http.Client
	cachedModels []llm.ModelMeta
	mu           sync.RWMutex
}

func NewAnthropic() *Anthropic {
	a := &Anthropic{client: &http.Client{}}
	if a.IsActive() {
		a.FetchModels()
	}
	return a
}

func (a *Anthropic) Name() string   { return "anthropic" }
func (a *Anthropic) IsActive() bool { return config.HasAPIKey("anthropic") }

func (a *Anthropic) Models() []llm.ModelMeta {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cachedModels
}

func (a *Anthropic) FetchModels() []llm.ModelMeta {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cachedModels = fetchAnthropicModels(config.GetAPIKey("anthropic"))
	return a.cachedModels
}

func (a *Anthropic) CompleteStream(ctx context.Context, req *llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	return llm.DoAnthropicStream(ctx, a.client, anthropicAPI,
		config.GetAPIKey("anthropic"), req,
		map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14"},
		nil, cb)
}

func (a *Anthropic) FormatUserMessage(text string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"role": "user", "content": text})
	return data
}

func (a *Anthropic) FormatUserMessageWithImages(text string, images []llm.ImageData) json.RawMessage {
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

func (a *Anthropic) FormatToolResults(results []llm.ToolResult) []json.RawMessage {
	var content []map[string]any
	for _, r := range results {
		block := map[string]any{
			"type": "tool_result", "tool_use_id": r.ID, "content": r.Output,
		}
		if r.IsErr {
			block["is_error"] = true
		}
		content = append(content, block)
	}
	data, _ := json.Marshal(map[string]any{"role": "user", "content": content})
	return []json.RawMessage{data}
}

const anthropicAPI = "https://api.anthropic.com/v1/messages"

func fetchAnthropicModels(tokenOrKey string) []llm.ModelMeta {
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

	var metas []llm.ModelMeta
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
		meta := llm.ModelMeta{
			ID: m.ID, DisplayName: m.DisplayName,
			ContextWindow: cw, MaxTokens: mt,
			Vision: m.Capabilities.Vision, Thinking: m.Capabilities.Thinking,
		}
		llm.ApplyRegistryPricing(&meta)
		metas = append(metas, meta)
	}
	return metas
}
