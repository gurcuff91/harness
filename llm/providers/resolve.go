package providers

import (
	"fmt"

	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/llm"
)

// Resolve returns the appropriate provider based on a "provider/model" identifier.
func Resolve(fullModel string) (llm.Provider, error) {
	provider, model := ParseModel(fullModel)

	switch provider {
	case "claude-oauth":
		p, err := NewClaudeOAuth(model)
		if err != nil {
			return nil, err
		}
		return p, nil

	case "anthropic":
		if !config.HasAPIKey("anthropic") {
			return nil, fmt.Errorf("anthropic not connected — use /connect anthropic")
		}
		return NewAnthropic(config.GetAPIKey("anthropic"), model), nil

	case "openai":
		if !config.HasAPIKey("openai") {
			return nil, fmt.Errorf("openai not connected — use /connect openai")
		}
		p := NewOpenAI(config.GetAPIKey("openai"), "https://api.openai.com/v1", model)
		return NewThinkingProviderForOpenAI(p, "openai", model), nil

	case "opencode-go":
		if !config.HasAPIKey("opencode-go") {
			return nil, fmt.Errorf("opencode-go not connected — use /connect opencode-go")
		}
		return NewOpenCodeGo(config.GetAPIKey("opencode-go"), model), nil

	case "ollama-cloud":
		if !config.HasAPIKey("ollama-cloud") {
			return nil, fmt.Errorf("ollama-cloud not connected — use /connect ollama-cloud")
		}
		return NewOllamaCloud(config.GetAPIKey("ollama-cloud"), model), nil

	case "ollama":
		if !OllamaAvailable() {
			return nil, fmt.Errorf("ollama not reachable — is it running?")
		}
		return NewOllama(model), nil

	default:
		return nil, fmt.Errorf("unknown provider %q in model %q — use /connect", provider, fullModel)
	}
}
