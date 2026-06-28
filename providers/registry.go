package providers

import (
	"fmt"
	"strings"
	"sync"
)

// All is the fixed registry of provider instances.
var All = []Provider{}

var initOnce sync.Once

func initRegistry() {
	All = []Provider{}
	if oauth, err := NewClaudeOAuth(); err == nil {
		All = append(All, oauth)
	}
	All = append(All,
		NewAnthropic(),
		NewOpenAI(),
		NewOpenCodeGo(),
		NewMiniMax(),
		NewOllamaCloud(),
		NewOllama(),
	)
}

func EnsureRegistry() {
	initOnce.Do(initRegistry)
}

// Resolve returns the provider and bare model ID for a "provider/model" string.
// It:
//  1. Splits "provider/model"
//  2. Finds the provider and checks credentials
//  3. Lazy-fetches models if cache is empty
//  4. Validates the model exists in that provider
//
// If only "provider" is given (no model), the first available model is used.
func Resolve(fullModel string) (Provider, string, error) {
	EnsureRegistry()

	// 1. Split "provider/model" — inline, no external dependency
	parts := strings.SplitN(fullModel, "/", 2)
	providerName := parts[0]
	modelID := ""
	if len(parts) == 2 {
		modelID = parts[1]
	}

	// 2. Find provider + check credentials
	var p Provider
	for _, candidate := range All {
		if candidate.Name() == providerName {
			p = candidate
			break
		}
	}
	if p == nil {
		return nil, "", fmt.Errorf("provider %q not found", providerName)
	}
	if !p.IsActive() {
		return nil, "", fmt.Errorf("provider %q is not active (missing credentials)", providerName)
	}

	// 3. Lazy fetch if cache is empty
	if len(p.Models()) == 0 {
		_, _ = p.FetchModels()
	}

	// 4. Default model or validate
	if modelID == "" {
		models := p.Models()
		if len(models) == 0 {
			return nil, "", fmt.Errorf("provider %q has no available models", providerName)
		}
		modelID = models[0].ID
	} else if p.ModelMeta(modelID) == nil {
		return nil, "", fmt.Errorf("model %q not found in provider %q", modelID, providerName)
	}

	return p, modelID, nil
}

// RefreshModels fetches models from all active providers.
// Used by the CLI on startup — not needed in SDK usage (Resolve handles lazy fetch).
func RefreshModels() {
	EnsureRegistry()
	for _, p := range All {
		if p.IsActive() {
			_, _ = p.FetchModels()
		}
	}
}

// RefreshProviderModels refreshes models for a single provider by name.
func RefreshProviderModels(providerName string) {
	EnsureRegistry()
	for _, p := range All {
		if p.Name() == providerName && p.IsActive() {
			_, _ = p.FetchModels()
			return
		}
	}
}
