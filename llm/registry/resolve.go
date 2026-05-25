package registry

import (
	"fmt"

	"github.com/gurcuff91/harness/llm"
	"github.com/gurcuff91/harness/llm/providers"
)

// Resolve returns the appropriate provider based on a "provider/model" identifier.
func Resolve(fullModel string) (llm.Provider, error) {
	provider, model := providers.ParseModel(fullModel)

	switch provider {
	case "claude-oauth":
		p, err := providers.NewClaudeOAuth(model)
		if err != nil {
			return nil, err
		}
		return p, nil

	case "anthropic":
		if !providers.HasAPIKey("anthropic") {
			return nil, fmt.Errorf("anthropic not connected — use /connect anthropic")
		}
		return providers.NewAnthropic(providers.GetAPIKey("anthropic"), model), nil

	case "openai":
		if !providers.HasAPIKey("openai") {
			return nil, fmt.Errorf("openai not connected — use /connect openai")
		}
		p := providers.NewOpenAI(providers.GetAPIKey("openai"), "https://api.openai.com/v1", model)
		return providers.NewThinkingProviderForOpenAI(p, "openai", model), nil

	case "opencode-go":
		if !providers.HasAPIKey("opencode-go") {
			return nil, fmt.Errorf("opencode-go not connected — use /connect opencode-go")
		}
		return providers.NewOpenCodeGo(providers.GetAPIKey("opencode-go"), model), nil

	case "ollama-cloud":
		if !providers.HasAPIKey("ollama-cloud") {
			return nil, fmt.Errorf("ollama-cloud not connected — use /connect ollama-cloud")
		}
		return providers.NewOllamaCloud(providers.GetAPIKey("ollama-cloud"), model), nil

	case "ollama":
		if !providers.OllamaAvailable() {
			return nil, fmt.Errorf("ollama not reachable — is it running?")
		}
		return providers.NewOllama(model), nil

	default:
		return nil, fmt.Errorf("unknown provider %q in model %q — use /connect", provider, fullModel)
	}
}

