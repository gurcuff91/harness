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
	"github.com/gurcuff91/harness/transport/cli"
)

func main() {
	// ── Model selection (priority: env > settings > auto-detect) ──
	model := os.Getenv("HARNESS_MODEL")
	if model == "" {
		model = config.GetSettingsManager().ActiveModel()
	}
	if model == "" {
		// Auto-detect first available provider/model
		providers.EnsureRegistry()
		for _, p := range providers.All {
			if !p.IsActive() {
				continue
			}
			if len(p.Models()) == 0 {
				p.FetchModels()
			}
			if len(p.Models()) > 0 {
				model = p.Name() + "/" + p.Models()[0].ID
				config.GetSettingsManager().SetActiveModel(model)
				break
			}
		}
	}

	// ── Create agent ──────────────────────────────────────────────
	// Store defaults to FileSessionStoreManager (~/.harness/agent/sessions/)
	a := agent.New(agent.AgentOptions{
		ThinkingLevel: config.GetSettingsManager().ThinkingLevel(),
	})

	// ── Graceful shutdown ─────────────────────────────────────────
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ── Launch CLI ────────────────────────────────────────────────
	t := cli.NewCLI(a, model)
	if err := t.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
