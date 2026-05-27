package providers

import (
	"fmt"
	"sync"

	llm "github.com/gurcuff91/harness/providers/llm"
)

// All is the fixed registry of provider instances.
// Created once: each provider reads its own credentials internally.
var All = []llm.Provider{}

var initOnce sync.Once

func initRegistry() {
	All = []llm.Provider{}
	if oauth, err := NewClaudeOAuth(); err == nil {
		All = append(All, oauth)
	}
	All = append(All,
		NewAnthropic(),
		NewOpenAI(),
		NewOpenCodeGo(),
		NewOllamaCloud(),
		NewOllama(),
	)
}

func EnsureRegistry() {
	initOnce.Do(initRegistry)
}

// Resolve returns the appropriate Provider and bare model ID for a "provider/model" string.
func Resolve(fullModel string) (llm.Provider, string, error) {
	EnsureRegistry()
	providerName, modelID := llm.ParseModel(fullModel)
	for _, p := range All {
		if p.Name() == providerName && p.IsActive() {
			return p, modelID, nil
		}
	}
	return nil, "", fmt.Errorf("provider %q not connected — use /connect", providerName)
}

// RefreshModels fetches models from all active providers.
func RefreshModels() {
	EnsureRegistry()
	for _, p := range All {
		if p.IsActive() {
			p.FetchModels()
		}
	}
}

// RefreshProviderModels refreshes models for a single provider.
func RefreshProviderModels(providerName string) {
	EnsureRegistry()
	for _, p := range All {
		if p.Name() == providerName && p.IsActive() {
			p.FetchModels()
			return
		}
	}
}
