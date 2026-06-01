package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/providers"
	// "github.com/gurcuff91/harness/transport/cli"  ← CLI kept, inactive
	"github.com/gurcuff91/harness/transport/tui"
)

func main() {
	// ── Model selection ──────────────────────────────────────────
	model := os.Getenv("HARNESS_MODEL")
	if model == "" {
		model = config.GetSettingsManager().ActiveModel()
	}
	if model == "" {
		providers.EnsureRegistry()
		for _, p := range providers.All {
			if !p.IsActive() { continue }
			if len(p.Models()) == 0 { p.FetchModels() }
			if len(p.Models()) > 0 {
				model = p.Name() + "/" + p.Models()[0].ID
				config.GetSettingsManager().SetActiveModel(model)
				break
			}
		}
	}

	// ── Create agent ──────────────────────────────────────────────
	a := agent.New(agent.AgentOptions{
		ThinkingLevel: config.GetSettingsManager().ThinkingLevel(),
	})

	// ── Launch TUI ────────────────────────────────────────────────
	// CLI is kept at transport/cli/
	p := tea.NewProgram(
		tui.New(a, model),
		tea.WithInputTTY(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
