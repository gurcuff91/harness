package providers

import "github.com/gurcuff91/harness/llm"

const ollamaCloudURL = "https://ollama.com/v1"

// NewOllamaCloud creates a provider for Ollama's cloud inference API.
func NewOllamaCloud(apiKey, model string) llm.Provider {
	p := NewOpenAI(apiKey, ollamaCloudURL, model)
	p.subscription = true // compute-based billing, not pay-per-token
	return newThinkingProvider(p, "ollama-cloud", model)
}
