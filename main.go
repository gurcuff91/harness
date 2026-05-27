package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/tools"
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

	// Refresh model cache FIRST — needed for auto-detection
	providers.RefreshModels()

	// ── Model selection (priority: env > settings > auto) ──
	if envModel := os.Getenv("HARNESS_MODEL"); envModel != "" {
		cfg.Model = envModel
	} else {
		cfg.Model = config.GetActiveModel()
		// If no model persisted yet, auto-detect from available providers
		if config.ReadSettings().Model == "" {
			providers.EnsureRegistry()
			for _, p := range providers.All {
				if !p.IsActive() || len(p.Models()) == 0 {
					continue
				}
				cfg.Model = p.Name() + "/" + p.Models()[0].ID
				config.SetActiveModel(cfg.Model)
				break
			}
		}
	}

	// Resolve provider from credentials
	provider, modelID, err := providers.Resolve(cfg.Model)
	if err != nil {
		hasAny := providers.OllamaAvailable()
		hasAny = hasAny || config.HasAPIKey("anthropic")
		hasAny = hasAny || config.HasAPIKey("openai")
		if tm, _ := providers.NewTokenManager(); tm != nil {
			if _, tokErr := tm.GetValidToken(); tokErr == nil {
				hasAny = true
			}
		}
		if !hasAny {
			fmt.Fprintf(os.Stderr, "No providers connected.\n")
			fmt.Fprintf(os.Stderr, "Use /connect claude-oauth  or  set env vars: HARNESS_MODEL, ANTHROPIC_API_KEY, OPENAI_API_KEY, OLLAMA_URL\n")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "provider error: %v\n", err)
		os.Exit(1)
	}

	// Build tool registry
	registry := tools.NewRegistry()
	registry.Register(tools.Bash())
	registry.Register(tools.ReadFile())
	registry.Register(tools.WriteFile())
	registry.Register(tools.Edit())
	registry.Register(tools.Fetch())

	// Create agent
	a := agent.New(provider, registry, agent.Options{
		SystemPrompt: cfg.SystemPrompt,
		Model:        modelID,
		MaxLoops:     cfg.MaxLoops,
		MaxTokens:    cfg.MaxTokens,
	})

	// Graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Launch CLI
	t := cli.NewCLI(a, provider)
	if err := t.Run(ctx, a, provider); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
