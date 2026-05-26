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
	"github.com/gurcuff91/harness/llm/providers"
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
			if config.HasOAuth("claude-oauth") {
				if n := providers.ModelCount("claude-oauth"); n > 0 {
					for fullName := range providers.AllModels() {
						if p, _ := providers.ParseModelKey(fullName); p == "claude-oauth" {
							cfg.Model = fullName
							config.SetActiveModel(cfg.Model)
							break
						}
					}
				}
			} else if providers.OllamaAvailable() {
				for fullName := range providers.AllModels() {
					if p, _ := providers.ParseModelKey(fullName); p == "ollama" {
						cfg.Model = fullName
						config.SetActiveModel(cfg.Model)
						break
					}
				}
			} else if config.HasAPIKey("anthropic") {
				cfg.Model = "anthropic/claude-sonnet-4-20250514"
				config.SetActiveModel(cfg.Model)
			} else if config.HasAPIKey("openai") {
				cfg.Model = "openai/gpt-4o"
				config.SetActiveModel(cfg.Model)
			}
		}
	}

	// Resolve provider from credentials
	provider, err := providers.Resolve(cfg.Model)
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
