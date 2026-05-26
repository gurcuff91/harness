package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/llm"
)

// NewOllama creates a provider for a local Ollama instance.
func NewOllama(model string) llm.Provider {
	url := config.GetOllamaURL()
	p := NewOpenAI("", url+"/v1", model)
	p.subscription = true // local compute, not pay-per-token
	return newThinkingProvider(p, "ollama", model)
}

// OllamaAvailable pings the Ollama server and returns true if reachable.
func OllamaAvailable() bool {
	url := config.GetOllamaURL()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/api/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// FetchOllamaModels returns installed models from the local Ollama instance.
func FetchOllamaModels() []ModelMeta {
	url := config.GetOllamaURL()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url + "/api/tags")
	if err != nil {
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Models []struct {
			Name    string `json:"name"`
			Details struct {
				Family       string `json:"family"`
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var models []ModelMeta
	for _, m := range result.Models {
		models = append(models, ModelMeta{
			ID:            m.Name,
			DisplayName:   fmt.Sprintf("%s (%s)", m.Name, m.Details.ParameterSize),
			ContextWindow: 128000,
			MaxTokens:     32000,
			Vision:        false,
		})
	}
	return models
}
