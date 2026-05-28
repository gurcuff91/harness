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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// ── Model selection (priority: env > settings > auto-detect) ──
	if envModel := os.Getenv("HARNESS_MODEL"); envModel != "" {
		cfg.Model = envModel
	} else {
		cfg.Model = config.GetActiveModel()
		// No model persisted yet — auto-detect first available provider/model
		if cfg.Model == "" {
			providers.EnsureRegistry()
			for _, p := range providers.All {
				if !p.IsActive() {
					continue
				}
				if len(p.Models()) == 0 {
					p.FetchModels()
				}
				if len(p.Models()) > 0 {
					cfg.Model = p.Name() + "/" + p.Models()[0].ID
					config.SetActiveModel(cfg.Model)
					break
				}
			}
		}
	}

	if cfg.Model == "" {
		fmt.Fprintf(os.Stderr, "No providers connected.\n")
		fmt.Fprintf(os.Stderr, "Set HARNESS_MODEL or configure credentials in ~/.harness/credentials.json\n")
		os.Exit(1)
	}

	// ── Create agent (resolves provider + validates model internally) ──
	a, err := agent.New(agent.AgentOptions{
		Model:        cfg.Model,
		SystemPrompt: cfg.SystemPrompt,
		MaxLoops:     cfg.MaxLoops,
		MaxTokens:    cfg.MaxTokens,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
		os.Exit(1)
	}

	// ── Graceful shutdown ──
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ── Launch CLI ──
	t := cli.NewCLI(a)
	if err := t.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
