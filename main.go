package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/llm/providers"
	"github.com/gurcuff91/harness/llm/registry"
	"github.com/gurcuff91/harness/tools"
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
		cfg.Model = providers.GetActiveModel()
		// If no model persisted yet, auto-detect from available providers
		if providers.ReadSettings().Model == "" {
			if providers.HasOAuth("claude-oauth") {
				if n := providers.ModelCount("claude-oauth"); n > 0 {
					// Pick first model
					for fullName := range providers.AllModels() {
						if p, _ := providers.ParseModelKey(fullName); p == "claude-oauth" {
							cfg.Model = fullName
							providers.SetActiveModel(cfg.Model)
							break
						}
					}
				}
			} else if providers.OllamaAvailable() {
				for fullName := range providers.AllModels() {
					if p, _ := providers.ParseModelKey(fullName); p == "ollama" {
						cfg.Model = fullName
						providers.SetActiveModel(cfg.Model)
						break
					}
				}
			} else if providers.HasAPIKey("anthropic") {
				cfg.Model = "anthropic/claude-sonnet-4-20250514"
				providers.SetActiveModel(cfg.Model)
			} else if providers.HasAPIKey("openai") {
				cfg.Model = "openai/gpt-4o"
				providers.SetActiveModel(cfg.Model)
			}
		}
	}

	// Resolve provider from credentials
	provider, err := registry.Resolve(cfg.Model)
	if err != nil {
		// Check if ANY provider is connected — if not, give helpful message
		hasAny := providers.OllamaAvailable()
		hasAny = hasAny || providers.HasAPIKey("anthropic")
		hasAny = hasAny || providers.HasAPIKey("openai")
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
