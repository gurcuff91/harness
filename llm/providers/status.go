package providers

import (
	"fmt"

	"github.com/gurcuff91/harness/config"
)

// ProviderStatus describes a provider and its current connection state.
type ProviderStatus struct {
	ID          string
	DisplayName string
	Connected   bool
	Note        string // e.g. "auto-connected", "connected (9 models)", etc.
}

// GetProviderStatuses returns the status of all known providers.
// This is the single source of truth for the transport layer.
func GetProviderStatuses() []ProviderStatus {
	var statuses []ProviderStatus

	// Claude OAuth
	claudeNote := "disconnected"
	claudeOK := false
	if tm, _ := NewTokenManager(); tm != nil {
		if _, err := tm.GetValidToken(); err == nil {
			claudeOK = true
			claudeNote = "connected"
			if n := ModelCount("claude-oauth"); n > 0 {
				claudeNote = itoa(n) + " models"
			}
		}
	}
	statuses = append(statuses, ProviderStatus{
		ID: "claude-oauth", DisplayName: "Claude OAuth",
		Connected: claudeOK, Note: claudeNote,
	})

	// Anthropic API key
	anthropicOK := config.HasAPIKey("anthropic")
	anthropicNote := "disconnected"
	if anthropicOK {
		anthropicNote = "connected"
		if n := ModelCount("anthropic"); n > 0 {
			anthropicNote = itoa(n) + " models"
		}
	}
	statuses = append(statuses, ProviderStatus{
		ID: "anthropic", DisplayName: "Anthropic",
		Connected: anthropicOK, Note: anthropicNote,
	})

	// OpenAI API key
	openaiOK := config.HasAPIKey("openai")
	openaiNote := "disconnected"
	if openaiOK {
		openaiNote = "connected"
		if n := ModelCount("openai"); n > 0 {
			openaiNote = itoa(n) + " models"
		}
	}
	statuses = append(statuses, ProviderStatus{
		ID: "openai", DisplayName: "OpenAI",
		Connected: openaiOK, Note: openaiNote,
	})

	// OpenCode Go
	openCodeOK := config.HasAPIKey("opencode-go")
	openCodeNote := "disconnected"
	if openCodeOK {
		openCodeNote = "connected"
		if n := ModelCount("opencode-go"); n > 0 {
			openCodeNote = itoa(n) + " models"
		}
	}
	statuses = append(statuses, ProviderStatus{
		ID: "opencode-go", DisplayName: "OpenCode Go",
		Connected: openCodeOK, Note: openCodeNote,
	})

	// Ollama Cloud
	ollamaCloudOK := config.HasAPIKey("ollama-cloud")
	ollamaCloudNote := "disconnected"
	if ollamaCloudOK {
		ollamaCloudNote = "connected"
		if n := ModelCount("ollama-cloud"); n > 0 {
			ollamaCloudNote = itoa(n) + " models"
		}
	}
	statuses = append(statuses, ProviderStatus{
		ID: "ollama-cloud", DisplayName: "Ollama Cloud",
		Connected: ollamaCloudOK, Note: ollamaCloudNote,
	})

	// Ollama local (auto-detected)
	ollamaOK := OllamaAvailable()
	ollamaNote := "disconnected"
	if ollamaOK {
		if n := ModelCount("ollama"); n > 0 {
			ollamaNote = "auto-connected (" + itoa(n) + " models)"
		} else {
			ollamaNote = "auto-connected (no models)"
		}
	}
	statuses = append(statuses, ProviderStatus{
		ID: "ollama", DisplayName: "Ollama",
		Connected: ollamaOK, Note: ollamaNote,
	})

	return statuses
}

// ModelGroups returns ordered groups of models for display.
// Each group has a label and list of ModelInfo for connected providers only.
type ModelGroup struct {
	Label  string
	Models []ModelInfo
}

func GetModelGroups(currentModel string) []ModelGroup {
	order := []struct{ id, label string }{
		{"claude-oauth", "Claude OAuth"},
		{"anthropic", "Anthropic API"},
		{"openai", "OpenAI API"},
		{"opencode-go", "OpenCode Go"},
		{"ollama-cloud", "Ollama Cloud"},
		{"ollama", "Ollama (local)"},
	}

	all := DetectAvailable(currentModel)
	byProvider := make(map[string][]ModelInfo)
	for _, m := range all {
		byProvider[m.Provider] = append(byProvider[m.Provider], m)
	}

	var groups []ModelGroup
	for _, entry := range order {
		if models, ok := byProvider[entry.id]; ok && len(models) > 0 {
			groups = append(groups, ModelGroup{Label: entry.label, Models: models})
		}
	}
	return groups
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
