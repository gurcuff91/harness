package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/providers"
	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// CLI is a terminal REPL transport.
type CLI struct {
	agent     *agent.Agent
	session   *agent.Session
	renderer  *Renderer
	modelName string // always "provider/model"
}

func NewCLI(a *agent.Agent) *CLI {
	return &CLI{agent: a}
}

func (c *CLI) Run(ctx context.Context) error {
	// Create session for the current working directory
	cwd, _ := os.Getwd()
	session, err := c.agent.NewSession(cwd)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	defer session.Close()
	c.session = session
	c.modelName = session.Meta().Model

	// Build renderer from model meta
	c.rebuildRenderer()
	session.Subscribe(c.renderer.Handle)

	c.printBanner()

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
			if c.handleCommand(ctx, input) {
				continue
			}
			fmt.Printf("  %s Unknown command: %s\n\n", C(Red, "✗"), C(Dim, input))
			continue
		}

		text, images := extractImages(input)
		if len(images) > 0 {
			fmt.Printf("  %s %s\n", C(Dim, "🖼"), C(Dim, fmt.Sprintf("%d image(s) attached", len(images))))
		}

		fmt.Println()
		_, err := c.session.Prompt(ctx, text, images)
		if err != nil {
			fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
		}
	}
}

func (c *CLI) printBanner() {
	fmt.Println()
	fmt.Println(C(Bold+Cyan, "  ╦ ╦╔═╗╦═╗╔╗╔╔═╗╔═╗╔═╗"))
	fmt.Println(C(Bold+Cyan, "  ╠═╣╠═╣╠╦╝║║║║╣ ╚═╗╚═╗"))
	fmt.Println(C(Bold+Cyan, "  ╩ ╩╩ ╩╩╚═╝╚╝╚═╝╚═╝╚═╝") + C(Dim, "  v0.5.0"))
	fmt.Println()
	fmt.Printf("  %s\n", C(Dim, "model: ")+C(BrightCyan, c.modelName))
	fmt.Printf("  %s\n", C(Dim, "/help for commands"))
	fmt.Println()
}

// rebuildRenderer creates a new Renderer for the current model.
func (c *CLI) rebuildRenderer() {
	rCfg := RendererConfig{
		ModelID: c.modelName,
	}
	if llm.ModelSupportsThinking(c.modelName) {
		if lvl := config.GetThinking(); lvl != "disable" {
			rCfg.ThinkingLevel = lvl
		}
	}
	c.renderer = NewRenderer(rCfg)
}

// ── Commands ─────────────────────────────────────────────────────────────

func (c *CLI) handleCommand(ctx context.Context, input string) bool {
	parts := strings.Fields(strings.ToLower(input))
	if len(parts) == 0 {
		return false
	}

	switch parts[0] {
	case "/clear":
		// Close current session and start a fresh one
		c.session.Close()
		cwd, _ := os.Getwd()
		session, err := c.agent.NewSession(cwd)
		if err != nil {
			fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
			return true
		}
		c.session = session
		session.Subscribe(c.renderer.Handle)
		fmt.Printf("  %s %s\n\n", C(Green, "✓"), C(Dim, "History cleared"))
		return true

	case "/exit", "/quit", "/q":
		fmt.Printf("  %s\n", C(Dim, "Goodbye."))
		os.Exit(0)

	case "/connect":
		c.handleConnect(parts)
		return true

	case "/thinking":
		c.handleThinking(parts)
		return true

	case "/model":
		if len(parts) < 2 {
			c.listModels()
		} else {
			c.switchModel(parts[1])
		}
		return true

	case "/help":
		c.printHelp()
		return true
	}
	return false
}

func (c *CLI) handleConnect(parts []string) {
	if len(parts) < 2 {
		// Show provider status
		statuses := []struct{ name, status string }{
			{"claude-oauth", func() string {
				if tm, _ := providers.NewTokenManager(); tm != nil {
					if _, err := tm.GetValidToken(); err == nil {
						return "connected"
					}
				}
				return "disconnected"
			}()},
			{"anthropic", connStatus(config.HasAPIKey("anthropic"))},
			{"openai", connStatus(config.HasAPIKey("openai"))},
			{"opencode-go", connStatus(config.HasAPIKey("opencode-go"))},
			{"ollama-cloud", connStatus(config.HasAPIKey("ollama-cloud"))},
			{"ollama", func() string {
				if providers.OllamaAvailable() {
					return "auto-connected"
				}
				return "disconnected"
			}()},
		}
		for _, s := range statuses {
			fmt.Printf("  %-18s (%s)\n", s.name, C(statusColor(s.status), s.status))
		}
		fmt.Println(C(Dim, "\n  Usage: /connect <provider>"))
		fmt.Println()
		return
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
	case "anthropic", "openai", "ollama-cloud", "opencode-go":
		if err := config.ConnectAPIKey(parts[1]); err != nil {
			fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
		} else {
			providers.RefreshProviderModels(parts[1])
			var n int
			for _, p := range providers.All {
				if p.Name() == parts[1] {
					n = len(p.Models())
					break
				}
			}
			fmt.Printf("  %s %s connected (%d models)\n\n", C(Green, "✓"), C(Green, parts[1]), n)
		}
	default:
		fmt.Printf("  %s Unknown provider: %s\n\n", C(Red, "✗"), C(Dim, parts[1]))
	}
}

func (c *CLI) handleThinking(parts []string) {
	if len(parts) < 2 {
		current := config.GetThinking()
		for _, l := range []string{"disable", "low", "medium", "high", "xhigh"} {
			marker := "  "
			if l == current {
				marker = C(Green, "● ")
			}
			fmt.Printf("  %s%s\n", marker, C(Dim, l))
		}
		fmt.Println(C(Dim, "\n  Usage: /thinking <level>"))
		fmt.Println()
		return
	}
	level := strings.ToLower(parts[1])
	valid := map[string]bool{"disable": true, "low": true, "medium": true, "high": true, "xhigh": true}
	if !valid[level] {
		fmt.Printf("  %s Invalid level: %s\n\n", C(Red, "✗"), level)
		fmt.Println(C(Dim, "  Valid: disable / low / medium / high / xhigh"))
		fmt.Println()
		return
	}
	config.SetThinking(level)
	c.session.SwitchThinking(level)
	if llm.ModelSupportsThinking(c.modelName) {
		c.renderer.SetThinkingLevel(level)
	}
	fmt.Printf("  %s Thinking level: %s\n\n", C(Green, "✓"), C(Green, level))
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
			fmt.Printf("  %s%s/%s\n", marker, C(Dim, m.Provider), C(Dim, m.ID))
		}
	}
	fmt.Println(C(Dim, "\n  Usage: /model <provider/model>"))
	fmt.Println()
}

func (c *CLI) switchModel(selector string) {
	// Normalize: if no "/" try to find provider prefix
	if !strings.Contains(selector, "/") {
		for _, p := range providers.All {
			if !p.IsActive() {
				continue
			}
			for _, m := range p.Models() {
				if m.ID == selector {
					selector = p.Name() + "/" + selector
					break
				}
			}
		}
	}

	if err := c.session.SwitchModel(selector); err != nil {
		fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
		return
	}

	c.modelName = c.session.Meta().Model
	c.rebuildRenderer()
	c.session.Subscribe(c.renderer.Handle)
	config.SetActiveModel(c.modelName)
	fmt.Printf("  %s Using %s\n\n", C(Green, "✓"), C(Green, c.modelName))
}

func (c *CLI) printHelp() {
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
	fmt.Println(C(Dim, "    /thinking [level]    — Show or set thinking level"))
	fmt.Println(C(Dim, "      levels: disable / low / medium / high / xhigh"))
	fmt.Println(C(Dim, "    /clear               — Reset conversation history"))
	fmt.Println(C(Dim, "    /exit                — Quit"))
	fmt.Println()
	fmt.Println(C(Bold, "  Images"))
	fmt.Println(C(Dim, "    Paste a file path:   describe /path/to/image.png"))
	fmt.Println(C(Dim, "    Clipboard:           Cmd+V pastes image path"))
	fmt.Println()
}

// ── Helpers ──────────────────────────────────────────────────────────────

func connStatus(active bool) string {
	if active {
		return "connected"
	}
	return "disconnected"
}

func statusColor(s string) string {
	switch s {
	case "connected", "auto-connected":
		return Green
	default:
		return Red
	}
}



var imagePathRe = regexp.MustCompile(`(?:^|\s)((?:/|\./|~/)[^\s]+\.(?:png|jpg|jpeg|gif|webp))`)

func extractImages(input string) (string, []types.ImageData) {
	matches := imagePathRe.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input, nil
	}
	var images []types.ImageData
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

// renderResponse kept for reference — streaming renderer handles output now.
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
