package providers

import (
	"fmt"
	"github.com/gurcuff91/harness/types"
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
			DisplayName: p.DisplayName(),
			Connected:   active,
			Note:        note,
		})
	}
	return statuses
}

// ModelGroup is a group of models from one provider for display.
type ModelGroup struct {
	Label  string
	Models []types.ModelInfo
}

// GetModelGroups returns ordered groups of models for transport display.
func GetModelGroups(currentModel string) []ModelGroup {
	EnsureRegistry()

	order := []string{"claude-oauth", "anthropic", "openai", "opencode-go", "minimax", "ollama-cloud", "ollama"}

	var groups []ModelGroup
	for _, name := range order {
		for _, p := range All {
			if !p.IsActive() || p.Name() != name {
				continue
			}
			var list []types.ModelInfo
			for _, m := range p.Models() {
				fullName := name + "/" + m.ID
				list = append(list, types.ModelInfo{
					ID:       m.ID,
					Provider: name,
					Active:   fullName == currentModel,
				})
			}
			if len(list) > 0 {
				groups = append(groups, ModelGroup{Label: p.DisplayName(), Models: list})
			}
		}
	}
	return groups
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
