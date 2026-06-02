package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/providers"
	// "github.com/gurcuff91/harness/transport/cli"  ← CLI (inactive)
	// "github.com/gurcuff91/harness/transport/tui"  ← Bubbletea TUI (inactive)
	tuiv2 "github.com/gurcuff91/harness/transport/tui-v2"
)

func main() {
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

	a := agent.New(agent.AgentOptions{
		ThinkingLevel: config.GetSettingsManager().ThinkingLevel(),
	})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	t := tuiv2.New(a, model)
	if err := t.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
