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

func NewCLI(a *agent.Agent, defaultModel string) *CLI {
	return &CLI{agent: a, modelName: defaultModel}
}

func (c *CLI) Run(ctx context.Context) error {
	// Try to create session with the saved active model
	if model := config.GetSettingsManager().ActiveModel(); model != "" {
		cwd, _ := os.Getwd()
		if session, err := c.agent.NewSession(cwd, model); err == nil {
			defer session.Close()
			c.session = session
			c.modelName = session.Meta().Model
			c.rebuildRenderer()
			session.Subscribe(c.renderer.Handle)
		}
		// err != nil → no provider active, session stays nil → banner shows hint
	}

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

		if c.session == nil {
			fmt.Printf("  %s No provider connected — use %s\n\n", C(Yellow, "⚠"), C(BrightCyan, "/connect"))
			continue
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
	fmt.Println(C(Bold+Cyan, "  ╩ ╩╩ ╩╩╚═╝╚╝╚═╝╚═╝╚═╝") + C(Dim, "  v0.6.0"))
	fmt.Println()
	if c.agent == nil {
		fmt.Printf("  %s\n", C(Yellow, "⚠  No provider connected"))
		fmt.Printf("  %s\n", C(Dim, "Use /connect to set up a provider"))
	} else {
		fmt.Printf("  %s\n", C(Dim, "model: ")+C(BrightCyan, c.modelName))
	}
	fmt.Printf("  %s\n", C(Dim, "/help for commands"))
	fmt.Println()
}

// rebuildRenderer creates a new Renderer for the current model.
func (c *CLI) rebuildRenderer() {
	rCfg := RendererConfig{
		ModelID: c.modelName,
	}
	// Use provider cache for authoritative thinking support detection
	var lookup func(string) *types.ModelMeta
	if c.session != nil {
		meta := c.session.Meta()
		if p, _, err := providers.Resolve(meta.Model); err == nil {
			lookup = p.ModelMeta
		}
	}
	if llm.ModelSupportsThinkingWithLookup(c.modelName, lookup) {
		if lvl := config.GetSettingsManager().ThinkingLevel(); lvl != "off" {
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
		session, err := c.agent.NewSession(cwd, c.modelName)
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

	case "/disconnect":
		c.handleDisconnect(parts)
		return true

	case "/thinking":
		c.handleThinking(parts)
		return true

	case "/model":
		if len(parts) < 2 {
			c.listModels()
		} else {
			c.switchModel(ctx, parts[1])
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
		// Show status for all known providers
		for _, p := range providers.All {
			status := "disconnected"
			if p.IsActive() {
				status = "connected"
			} else if p.CredentialType() == types.CredTypeNone {
				status = "auto-connected"
			}
			fmt.Printf("  %-18s (%s)\n", p.Name(), C(statusColor(status), status))
		}
		fmt.Println(C(Dim, "\n  Usage: /connect <provider>"))
		fmt.Println()
		return
	}

	name := parts[1]
	var target providers.Provider
	for _, p := range providers.All {
		if p.Name() == name {
			target = p
			break
		}
	}
	if target == nil {
		fmt.Printf("  %s Unknown provider: %s\n\n", C(Red, "✗"), C(Dim, name))
		return
	}

	switch target.CredentialType() {
	case types.CredTypeNone:
		fmt.Printf("  %s %s is auto-detected, no credentials needed\n\n", C(Green, "✓"), name)

	case types.CredTypeOAuth:
		fmt.Println(C(Dim, "\n  Connecting via OAuth..."))
		fmt.Println()
		token := providers.RunOAuthFlow()
		if token == nil {
			fmt.Printf("  %s OAuth flow failed\n\n", C(Red, "✗"))
			return
		}
		if err := target.SaveCredentials(*token); err != nil {
			fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
			return
		}
		fmt.Printf("  %s Connected!\n\n", C(Green, "✓"))
		c.tryInitSession(name)

	case types.CredTypeAPIKey:
		key := c.readMasked(fmt.Sprintf("Enter %s API key", name))
		if key == "" {
			fmt.Printf("  %s Cancelled\n\n", C(Red, "✗"))
			return
		}
		if err := target.SaveCredentials(types.APIKeyCredentials(key)); err != nil {
			fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
			return
		}
		fmt.Printf("  %s %s connected (%d models)\n\n", C(Green, "✓"), C(Green, name), len(target.Models()))
		c.tryInitSession(name)
	}
}

func (c *CLI) handleDisconnect(parts []string) {
	if len(parts) < 2 {
		fmt.Println(C(Dim, "  Usage: /disconnect <provider>"))
		fmt.Println()
		return
	}
	name := parts[1]
	var target providers.Provider
	for _, p := range providers.All {
		if p.Name() == name {
			target = p
			break
		}
	}
	if target == nil {
		fmt.Printf("  %s Unknown provider: %s\n\n", C(Red, "✗"), C(Dim, name))
		return
	}
	if target.CredentialType() == types.CredTypeNone {
		fmt.Printf("  %s %s is auto-detected — cannot disconnect\n\n", C(Yellow, "⚠"), name)
		return
	}
	if err := target.ClearCredentials(); err != nil {
		fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
		return
	}
	// If active session was using this provider, close it
	if c.session != nil && c.modelName != "" {
		providerName := strings.SplitN(c.modelName, "/", 2)[0]
		if providerName == name {
			c.session.Close()
			c.session = nil
			c.modelName = ""
			fmt.Printf("  %s %s disconnected — session closed\n\n", C(Green, "✓"), C(Yellow, name))
			return
		}
	}
	fmt.Printf("  %s %s disconnected\n\n", C(Green, "✓"), C(Yellow, name))
}

func (c *CLI) handleThinking(parts []string) {
	if len(parts) < 2 {
		current := config.GetSettingsManager().ThinkingLevel()
		for _, l := range []string{"off", "low", "medium", "high", "xhigh"} {
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
	valid := map[string]bool{"off": true, "low": true, "medium": true, "high": true, "xhigh": true}
	if !valid[level] {
		fmt.Printf("  %s Invalid level: %s\n\n", C(Red, "✗"), level)
		fmt.Println(C(Dim, "  Valid: disable / low / medium / high / xhigh"))
		fmt.Println()
		return
	}
	config.GetSettingsManager().SetThinkingLevel(level)
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

func (c *CLI) switchModel(ctx context.Context, selector string) {
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

	if err := c.session.SwitchModel(ctx, selector); err != nil {
		fmt.Printf("  %s %s\n\n", C(Red, "✗"), C(Red, err.Error()))
		return
	}

	c.modelName = c.session.Meta().Model
	c.rebuildRenderer()
	c.session.Subscribe(c.renderer.Handle)
	config.GetSettingsManager().SetActiveModel(c.modelName)
	fmt.Printf("  %s Using %s\n\n", C(Green, "✓"), C(Green, c.modelName))
}

func (c *CLI) printHelp() {
	fmt.Println()
	fmt.Println(C(Bold, "  Providers"))
	fmt.Println(C(Dim, "    /connect              — List providers and status"))
	fmt.Println(C(Dim, "    /connect <provider>      — Connect a provider"))
	fmt.Println(C(Dim, "    /disconnect <provider>   — Remove credentials for a provider"))
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

// tryInitSession creates a session after a successful /connect.
func (c *CLI) tryInitSession(providerName string) {
	// Find first available model for this provider
	var model string
	for _, p := range providers.All {
		if p.Name() == providerName && p.IsActive() && len(p.Models()) > 0 {
			model = p.Name() + "/" + p.Models()[0].ID
			break
		}
	}
	if model == "" {
		return
	}
	cwd, _ := os.Getwd()
	session, err := c.agent.NewSession(cwd, model)
	if err != nil {
		return
	}
	if c.session != nil {
		c.session.Close()
	}
	c.session = session
	c.modelName = model
	c.rebuildRenderer()
	session.Subscribe(c.renderer.Handle)
	config.GetSettingsManager().SetActiveModel(model)
	fmt.Printf("  %s Active model: %s\n\n", C(Green, "✓"), C(BrightCyan, model))
}

func (c *CLI) readMasked(prompt string) string {
	fmt.Printf("\n  %s: ", prompt)
	var key []byte
	buf := make([]byte, 1)
	for {
		os.Stdin.Read(buf)
		if buf[0] == 13 || buf[0] == 10 {
			break
		}
		if buf[0] == 3 {
			fmt.Println()
			return ""
		}
		if buf[0] == 127 || buf[0] == 8 {
			if len(key) > 0 {
				key = key[:len(key)-1]
				fmt.Print("\b \b")
			}
			continue
		}
		key = append(key, buf[0])
		fmt.Print("*")
	}
	fmt.Println()
	return string(key)
}

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
