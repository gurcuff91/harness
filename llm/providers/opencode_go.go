package providers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gurcuff91/harness/llm"
)

const openCodeGoURL = "https://opencode.ai/zen/go/v1"

// NewOpenCodeGo creates a provider for OpenCode Go.
func NewOpenCodeGo(apiKey, model string) llm.Provider {
	p := NewOpenAI(apiKey, openCodeGoURL, model)
	return newThinkingProvider(p, "opencode-go", model)
}

// fetchOpenCodeGoModels fetches the model list from OpenCode Go.
// /v1/models is public (no auth required).
func fetchOpenCodeGoModels() []ModelMeta {
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", openCodeGoURL+"/models", nil)

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var models []ModelMeta
	for _, m := range result.Data {
		meta := ModelMeta{
			ID:            m.ID,
			ContextWindow: inferContextWindow(m.ID),
			MaxTokens:     32000,
			Vision:        inferVision(m.ID),
		}
		models = append(models, enrichMeta(meta))
	}
	return models
}
