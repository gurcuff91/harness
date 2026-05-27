package providers

import (
	"fmt"

	llm "github.com/gurcuff91/harness/providers/llm"
)

// ProviderStatus describes a provider and its current connection state.
type ProviderStatus struct {
	ID          string
	DisplayName string
	Connected   bool
	Note        string
}

// GetProviderStatuses returns the status of all known providers from the registry.
func GetProviderStatuses() []ProviderStatus {
	EnsureRegistry()

	labels := map[string]string{
		"claude-oauth":  "Claude OAuth",
		"anthropic":     "Anthropic",
		"openai":        "OpenAI",
		"opencode-go":   "OpenCode Go",
		"ollama-cloud":  "Ollama Cloud",
		"ollama":        "Ollama",
	}

	var statuses []ProviderStatus
	for _, p := range All {
		active := p.IsActive()
		note := "disconnected"
		if active {
			note = "connected"
			if n := len(p.Models()); n > 0 {
				note = itoa(n) + " models"
			}
		}
		statuses = append(statuses, ProviderStatus{
			ID:          p.Name(),
			DisplayName: labels[p.Name()],
			Connected:   active,
			Note:        note,
		})
	}
	return statuses
}

// ModelGroup is a group of models from one provider for display.
type ModelGroup struct {
	Label  string
	Models []llm.ModelInfo
}

// GetModelGroups returns ordered groups of models for transport display.
func GetModelGroups(currentModel string) []ModelGroup {
	EnsureRegistry()

	labels := map[string]string{
		"claude-oauth":  "Claude OAuth",
		"anthropic":     "Anthropic API",
		"openai":        "OpenAI API",
		"opencode-go":   "OpenCode Go",
		"ollama-cloud":  "Ollama Cloud",
		"ollama":        "Ollama (local)",
	}

	order := []string{"claude-oauth", "anthropic", "openai", "opencode-go", "ollama-cloud", "ollama"}

	var groups []ModelGroup
	for _, name := range order {
		for _, p := range All {
			if !p.IsActive() || p.Name() != name {
				continue
			}
			var list []llm.ModelInfo
			for _, m := range p.Models() {
				fullName := name + "/" + m.ID
				list = append(list, llm.ModelInfo{
					Name:     m.ID,
					Provider: name,
					Active:   fullName == currentModel,
				})
			}
			if len(list) > 0 {
				groups = append(groups, ModelGroup{Label: labels[name], Models: list})
			}
		}
	}
	return groups
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
