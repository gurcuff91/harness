package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/llm"
	"github.com/gurcuff91/harness/llm/providers"
	"github.com/gurcuff91/harness/llm/registry"

)

// CLI is a terminal REPL transport with rich UX rendering.
type CLI struct {
	agent      *agent.Agent
	renderer   *Renderer
	modelName  string
	
}

func NewCLI(a *agent.Agent, provider llm.Provider) *CLI {
	// activeModel is always "provider/model" format
	activeModel := providers.GetActiveModel()

	rCfg := modelPricing(provider.Model())
	// Use full "provider/model" as the display label — no parentheses
	rCfg.ProviderName = ""
	rCfg.ModelID = activeModel
	// Only show thinking level for providers that support it
	if strings.HasPrefix(activeModel, "claude-oauth/") || strings.HasPrefix(activeModel, "anthropic/") {
		rCfg.ThinkingLevel = providers.GetThinking()
	} else {
		rCfg.ThinkingLevel = ""
	}

	r := NewRenderer(rCfg)
	a.OnEvent(r.Handle)

	return &CLI{
		agent:      a,
		renderer:   r,
		modelName:  activeModel,
	}
}

func (c *CLI) Run(ctx context.Context, a *agent.Agent, provider llm.Provider) error {
	c.printBanner()

	userID := "cli-user"

	ri := newRawInput(func() string {
		path := checkClipboardImage()
		if path != "" {
			return path + " "
		}
		return ""
	})

	for {
		select {
		case <-ctx.Done():
			fmt.Println(C(Dim, "\nShutting down."))
			return nil
		default:
		}

		fmt.Print(C(BrightGreen, "→ "))

		input, quit := ri.ReadLine()
		if quit {
			fmt.Println(C(Dim, "Goodbye."))
			return nil
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		// Handle slash commands
		if strings.HasPrefix(input, "/") {
			if c.handleCommand(input, userID) {
				continue
			}
			// Unknown command — don't send to agent
			fmt.Printf("  %s Unknown command: %s\n\n", C(Red, "✗"), C(Dim, input))
			continue
		}

		text, images := extractImages(input)
		if len(images) > 0 {
			fmt.Printf("  %s %s\n", C(Dim, "🖼"), C(Dim, fmt.Sprintf("%d image(s) attached", len(images))))
		}

		start := time.Now()
		fmt.Println()

		_, err := c.agent.Chat(ctx, userID, text, images)
		_ = time.Since(start)

		if err != nil {
			fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
			continue
		}
		// Streaming: renderer handled it
	}
}

func (c *CLI) printBanner() {
	fmt.Println()
	fmt.Println(C(Bold+Cyan, "  ╦ ╦╔═╗╦═╗╔╗╔╔═╗╔═╗╔═╗"))
	fmt.Println(C(Bold+Cyan, "  ╠═╣╠═╣╠╦╝║║║║╣ ╚═╗╚═╗"))
	fmt.Println(C(Bold+Cyan, "  ╩ ╩╩ ╩╩╚═╝╚╝╚═╝╚═╝╚═╝") + C(Dim, "  v0.1.0"))
	fmt.Println()
	fmt.Printf("  %s\n", C(Dim, "model: ")+C(BrightCyan, c.modelName))
	fmt.Printf("  %s\n", C(Dim, "/help for commands"))
	fmt.Println()
}

func (c *CLI) handleCommand(input, userID string) bool {
	parts := strings.Fields(strings.ToLower(input))
	if len(parts) == 0 {
		return false
	}

	switch parts[0] {
	case "/clear":
		c.agent.ClearHistory(userID)
		fmt.Printf("  %s %s\n\n", C(Green, "✓"), C(Dim, "History cleared"))
		return true

	case "/exit", "/quit", "/q":
		fmt.Printf("  %s\n", C(Dim, "Goodbye."))
		os.Exit(0)

	case "/connect":
		if len(parts) < 2 {
			ollamaCloudSt := "disconnected"
			if providers.HasAPIKey("ollama-cloud") { ollamaCloudSt = "connected" }
			openCodeSt := "disconnected"
			if providers.HasAPIKey("opencode-go") { openCodeSt = "connected" }
			claudeSt := "disconnected"
			if tm, _ := providers.NewTokenManager(); tm != nil {
				if _, err := tm.GetValidToken(); err == nil { claudeSt = "connected" }
			}
			anthropicSt := "disconnected"
			if providers.HasAPIKey("anthropic") { anthropicSt = "connected" }
			openaiSt := "disconnected"
			if providers.HasAPIKey("openai") { openaiSt = "connected" }
			ollamaSt := "disconnected"
			if providers.OllamaAvailable() { ollamaSt = "auto-connected" }

			fmt.Printf("  claude-oauth   (%s)\n", C(statusColor(claudeSt), claudeSt))
			fmt.Printf("  anthropic      (%s)\n", C(statusColor(anthropicSt), anthropicSt))
			fmt.Printf("  openai         (%s)\n", C(statusColor(openaiSt), openaiSt))
			fmt.Printf("  opencode-go    (%s)\n", C(statusColor(openCodeSt), openCodeSt))
			fmt.Printf("  ollama-cloud   (%s)\n", C(statusColor(ollamaCloudSt), ollamaCloudSt))
			fmt.Printf("  ollama         (%s)\n", C(statusColor(ollamaSt), ollamaSt))
			fmt.Println(C(Dim, "\n  Usage: /connect <provider>"))
			fmt.Println()
			return true
		}
		switch parts[1] {
		case "claude-oauth":
			fmt.Println(C(Dim, "\n  Connecting to Claude OAuth..."))
			fmt.Println()
			if err := providers.Login(); err != nil {
				fmt.Printf("  %s %s\n", C(Red, "✗"), C(Red, err.Error()))
			} else {
				fmt.Printf("\n  %s Connected! Restart to apply.\n", C(Green, "✓"))
			}
			fmt.Println()
			return true
		case "anthropic", "openai", "ollama-cloud", "opencode-go":
			if err := providers.ConnectAPIKey(parts[1]); err != nil {
				fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
			} else {
				// Refresh model cache synchronously so /model shows them immediately
				providers.RefreshProviderModels(parts[1])
				n := providers.ModelCount(parts[1])
				if n > 0 {
					fmt.Printf("  %s %s connected (%d models)\n\n", C(Green, "✓"), C(Green, parts[1]), n)
				} else {
					fmt.Printf("  %s %s connected\n\n", C(Green, "✓"), C(Green, parts[1]))
				}
			}
			return true
		default:
			fmt.Printf("  %s Unknown provider: %s\n\n", C(Red, "✗"), C(Dim, parts[1]))
			return true
		}

	case "/thinking":
		if len(parts) < 2 {
			current := providers.GetThinking()
			levels := []string{"disable", "low", "medium", "high", "xhigh"}
			for _, l := range levels {
				marker := "  "
				if l == current {
					marker = C(Green, "● ")
				}
				fmt.Printf("  %s%s\n", marker, C(Dim, l))
			}
			fmt.Println(C(Dim, "\n  Usage: /thinking <level>"))
			fmt.Println()
		} else {
			level := strings.ToLower(parts[1])
			valid := map[string]bool{"disable": true, "low": true, "medium": true, "high": true, "xhigh": true}
			if !valid[level] {
				fmt.Printf("  %s Invalid level: %s\n\n", C(Red, "✗"), level)
				fmt.Println(C(Dim, "  Valid: disable / low / medium / high / xhigh"))
				fmt.Println()
			} else {
				providers.SetThinking(level)
				fmt.Printf("  %s Thinking level: %s\n\n", C(Green, "✓"), C(Green, level))
			}
		}
		return true

	case "/model":
		if len(parts) < 2 {
			c.listModels()
		} else {
			c.switchModel(parts[1])
		}
		return true

	case "/help":
		fmt.Println()
		fmt.Println(C(Bold, "  Providers"))
		fmt.Println(C(Dim, "    /connect              — List providers and status"))
		fmt.Println(C(Dim, "    /connect <provider>   — Connect a provider"))
		fmt.Println(C(Dim, "      providers: claude-oauth, anthropic, openai, opencode-go, ollama-cloud"))
		fmt.Println(C(Dim, "      ollama is auto-detected (no connect needed)"))
		fmt.Println()
		fmt.Println(C(Bold, "  Models"))
		fmt.Println(C(Dim, "    /model               — List available models"))
		fmt.Println(C(Dim, "    /model <prov/model>  — Switch active model"))
		fmt.Println()
		fmt.Println(C(Bold, "  Session"))
		fmt.Println(C(Dim, "    /thinking [level]     — Show or set thinking level"))
		fmt.Println(C(Dim, "      levels: disable / low / medium / high / xhigh"))
		fmt.Println(C(Dim, "    /clear               — Reset conversation history"))
		fmt.Println(C(Dim, "    /exit                — Quit"))
		fmt.Println()
		fmt.Println(C(Bold, "  Images"))
		fmt.Println(C(Dim, "    Paste a file path:    describe /path/to/image.png"))
		fmt.Println(C(Dim, "    Clipboard:           Cmd+V pastes image path"))
		fmt.Println()
		fmt.Println(C(Bold, "  Env vars"))
		fmt.Println(C(Dim, "    ANTHROPIC_API_KEY     — Anthropic API key"))
		fmt.Println(C(Dim, "    OPENAI_API_KEY        — OpenAI API key"))
		fmt.Println(C(Dim, "    OPENCODE_GO_API_KEY   — OpenCode Go API key"))
		fmt.Println(C(Dim, "    OLLAMA_API_KEY        — Ollama Cloud API key"))
		fmt.Println(C(Dim, "    OLLAMA_URL            — Ollama server URL (default: localhost:11434)"))
		fmt.Println(C(Dim, "    HARNESS_MODEL         — Override default model (provider/model)"))
		fmt.Println(C(Dim, "    HARNESS_THINKING        — Thinking level (disable/low/medium/high/xhigh)"))
		fmt.Println()
		return true
	}
	return false
}

func (c *CLI) listModels() {
	groups := providers.GetModelGroups(c.modelName)
	if len(groups) == 0 {
		fmt.Printf("  %s No models available.\n\n", C(Red, "✗"))
		return
	}
	for _, g := range groups {
		fmt.Printf("  %s\n", C(Bold+Dim, g.Label))
		for _, m := range g.Models {
			marker := "  "
			if m.Active {
				marker = C(Green, "● ")
			}
			fmt.Printf("  %s%s/%s\n", marker, C(Dim, m.Provider), C(Dim, m.Name))
		}
	}
	fmt.Println(C(Dim, "\n  Usage: /model <provider/model>"))
	fmt.Println()
}

func (c *CLI) switchModel(selector string) {
	models := providers.DetectAvailable(c.modelName)
	var target *providers.ModelInfo
	for _, m := range models {
		if m.Provider+"/"+m.Name == selector || m.Name == selector {
			target = &m
			break
		}
	}
	if target == nil {
		fmt.Printf("  %s Not found: %s\n", C(Red, "✗"), C(Dim, selector))
		fmt.Println()
		return
	}
	newProvider, err := registry.Resolve(target.Provider + "/" + target.Name)
	if err != nil {
		fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
		return
	}
	c.agent.SetProvider(newProvider)
	fullModel := target.Provider + "/" + target.Name
	c.modelName = fullModel
	rCfg := modelPricing(target.Name)
	rCfg.ProviderName = ""
	rCfg.ModelID = fullModel
	if target.Provider == "claude-oauth" || target.Provider == "anthropic" {
		rCfg.ThinkingLevel = providers.GetThinking()
	}
	c.renderer = NewRenderer(rCfg)
	c.agent.OnEvent(c.renderer.Handle)
	fmt.Printf("  %s Using %s\n\n", C(Green, "✓"), C(Green, fullModel))
	providers.SetActiveModel(fullModel)
}


func statusColor(s string) string {
	switch s {
	case "connected", "auto-connected":
		return Green
	default:
		return Red
	}
}

func (c *CLI) renderResponse(text string, dur time.Duration) {
	if text == "" {
		return
	}
	fmt.Println()
	for _, line := range strings.Split(text, "\n") {
		fmt.Printf("  %s %s\n", C(BrightCyan, "│"), line)
	}
	fmt.Printf("  %s\n  %s %s\n",
		C(BrightCyan, "│"),
		C(BrightCyan, "╰"),
		C(Gray, fmt.Sprintf("%.1fs", dur.Seconds())),
	)
	fmt.Println()
}

// subPricingProviders are providers where cost is a reference metric, not actual spend.
// claude-oauth = flat subscription, ollama/ollama-cloud = local or compute-based.
var subPricingProviders = map[string]bool{
	"claude-oauth":  true,
	"opencode-go":   true,
	"ollama":        true,
	"ollama-cloud":  true,
}

func modelPricing(model string) RendererConfig {
	cfg := RendererConfig{
		ContextWindow: 128000, // safe default
	}

	// Find ModelMeta — try all provider prefixes against the in-memory cache.
	// Track which provider matched so we can set SubPricing correctly.
	var meta *providers.ModelMeta
	for _, prefix := range []string{"opencode-go", "ollama-cloud", "ollama", "openai", "anthropic", "claude-oauth"} {
		if m := providers.GetModelMeta(prefix + "/" + model); m != nil {
			meta = m
			cfg.SubPricing = subPricingProviders[prefix]
			break
		}
	}
	// Fallback: try registry directly (model not yet in cache)
	if meta == nil {
		meta = providers.LookupModel(model)
	}

	if meta != nil {
		if meta.ContextWindow > 0 {
			cfg.ContextWindow = meta.ContextWindow
		}
		// Pricing from registry — zero means unknown, footer will hide $
		cfg.CostInput = meta.InputCost
		cfg.CostOutput = meta.OutputCost
		cfg.CostCacheRead = meta.CacheReadCost
		cfg.CostCacheWrite = meta.CacheWriteCost
	}
	return cfg
}

var imagePathRe = regexp.MustCompile(`(?:^|\s)((?:/|\./|~/)[^\s]+\.(?:png|jpg|jpeg|gif|webp))`)

func extractImages(input string) (string, []llm.ImageData) {
	matches := imagePathRe.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input, nil
	}

	var images []llm.ImageData
	text := input

	for _, m := range matches {
		path := strings.TrimSpace(m[1])
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = home + path[1:]
		}

		img, err := llm.LoadImage(path)
		if err != nil {
			fmt.Printf("  %s %s\n", C(Red, "⚠"), C(Red, fmt.Sprintf("image: %v", err)))
			continue
		}
		images = append(images, img)
		text = strings.Replace(text, m[1], "", 1)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		text = "What's in this image?"
	}
	return text, images
}
