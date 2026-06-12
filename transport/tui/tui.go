package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/gurcuff91/harness/agent"
)

// ── Color palette ──────────────────────────────────────────────────────────
const (
	clrUser        = "[#5fafd7]" // user input (cyan medium)
	clrThinking    = "[::d]"     // thinking block (ANSI dim)
	clrToolName    = "[#ffaf5f]" // tool name (amber/orange)
	clrToolArgs    = "[#767676]" // tool args / metadata (neutral gray)
	clrCompact     = "[#af87ff]" // compact operation (purple)
	clrToolOK      = "[#5faf5f]" // tool result success (green)
	clrToolErr     = "[#ff5f5f]" // tool result error (red)
	clrWarn        = "[#ffff5f]" // warning (yellow)
	clrError       = "[#ff5f5f]" // error message (red)
	clrFooter      = "[::d]"     // footer / info (ANSI dim)
	clrConfirm     = "[::d]"     // command confirmation
	clrPlaceholder = "[::d]"     // input placeholder
	clrReset       = "[-:-:-]"   // reset color + attributes
)

// ── Palette types ─────────────────────────────────────────────────────────

type paletteItem struct {
	name string // displayed
	desc string // displayed (right side)
	id   string // internal key (e.g. session ID) — not displayed
}

type paletteLevel struct {
	items     []paletteItem
	filter    string
	sel       int
	parentCmd string
}

type palette struct {
	open   bool
	levels []paletteLevel
}

func (p *palette) current() *paletteLevel {
	if len(p.levels) == 0 {
		return nil
	}
	return &p.levels[len(p.levels)-1]
}

func (p *palette) depth() int { return len(p.levels) }

func (p *palette) filtered() []paletteItem {
	lv := p.current()
	if lv == nil {
		return nil
	}
	if lv.filter == "" {
		return lv.items
	}
	f := strings.ToLower(lv.filter)
	var out []paletteItem
	for _, it := range lv.items {
		if strings.Contains(strings.ToLower(it.name), f) ||
			strings.Contains(strings.ToLower(it.desc), f) {
			out = append(out, it)
		}
	}
	return out
}

func (p *palette) setFilter(f string) {
	if lv := p.current(); lv != nil {
		lv.filter = f
		if lv.sel >= len(p.filtered()) {
			lv.sel = 0
		}
	}
}

func (p *palette) moveUp() {
	lv := p.current()
	if lv == nil {
		return
	}
	n := len(p.filtered())
	if n == 0 {
		return
	}
	lv.sel--
	if lv.sel < 0 {
		lv.sel = n - 1
	}
}

func (p *palette) moveDown() {
	lv := p.current()
	if lv == nil {
		return
	}
	n := len(p.filtered())
	if n == 0 {
		return
	}
	lv.sel++
	if lv.sel >= n {
		lv.sel = 0
	}
}

func (p *palette) selected() (paletteItem, bool) {
	items := p.filtered()
	lv := p.current()
	if lv == nil || len(items) == 0 || lv.sel >= len(items) {
		return paletteItem{}, false
	}
	return items[lv.sel], true
}

func (p *palette) openRoot(items []paletteItem) {
	p.open = true
	p.levels = []paletteLevel{{items: items}}
}

func (p *palette) pushSub(parentCmd string, items []paletteItem) {
	p.levels = append(p.levels, paletteLevel{items: items, parentCmd: parentCmd})
}

func (p *palette) popLevel() bool {
	if len(p.levels) > 1 {
		p.levels = p.levels[:len(p.levels)-1]
		return true
	}
	return false
}

func (p *palette) close() {
	p.open = false
	p.levels = nil
}

// ── TUI ───────────────────────────────────────────────────────────────────

type TUI struct {
	agent       *agent.Agent
	client      *Client
	addr        string
	sessionID   string
	sessionName string
	model       string
	overrideModel    string // from --model flag (takes priority over settings)
	overrideThinking string // from --thinking flag
	resumeID         string // from --resume flag (resume instead of create)
	sseCancel   context.CancelFunc

	// tview
	app       *tview.Application
	output    *tview.TextView
	spinner   *tview.TextView
	inputTV   *tview.TextView
	paletteTV *tview.TextView
	spacerTop *tview.Box
	spacerBot *tview.Box
	flex      *tview.Flex
	info      *tview.TextView
	footer    *tview.TextView

	// input state
	inputBuf string
	pal      palette

	// session state
	thinking       string
	isSubscription bool
	sessionCmds    []CommandDef

	// state
	spinning     bool
	spinnerStop  chan struct{}
	compactStart time.Time
	queueCount   int      // number of prompts waiting in the session queue
	localQueue   []string // pending user messages (for display)
	stats        tokensInfo
}

type tokensInfo struct {
	input, output         int
	cacheRead, cacheWrite int
	cost                  float64
	contextPct            float64
	contextWin            int
}

func New(a *agent.Agent) *TUI {
	return &TUI{agent: a}
}

// SetFlags applies CLI flags (overrides settings).
func (t *TUI) SetFlags(model, thinking, resumeID string) {
	t.overrideModel = model
	t.overrideThinking = thinking
	t.resumeID = resumeID
}

func (t *TUI) Run(ctx context.Context) error {
	srv, addr, err := startInternalServer(t.agent)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	t.addr = addr
	defer srv.Close()
	t.client = NewClient(addr)

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	t.app = tview.NewApplication()
	t.buildUI()

	t.spinnerStop = make(chan struct{})
	go t.spinnerLoop()

	go func() {
		<-ctx.Done()
		t.app.Stop()
	}()

	// autoConnect after app is running and server is ready
	go func() {
		time.Sleep(150 * time.Millisecond)
		t.autoConnect()
	}()

	return t.app.EnableMouse(true).Run()
}

// ── Layout ────────────────────────────────────────────────────────────────

func (t *TUI) buildUI() {
	// Output: scrollable, dynamic colors, tracks end
	t.output = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(true).
		SetWrap(true).
		SetChangedFunc(func() {
			if t.spinning {
				t.output.ScrollToEnd()
			}
			t.app.Draw()
		})
	t.output.SetBorder(false)
	t.output.SetBackgroundColor(tcell.ColorDefault)

	// Input: plain TextView — no background color issues
	t.inputTV = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(false).
		SetChangedFunc(func() { t.app.Draw() })
	t.inputTV.SetBorder(false)
	t.inputTV.SetBackgroundColor(tcell.ColorDefault)
	t.renderInput()

	// App-level key capture — fires for every key regardless of focus
	t.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return t.handleKey(event)
	})

	// Spinner: always 3 lines (blank + text + blank)
	t.spinner = tview.NewTextView().SetDynamicColors(true)
	t.spinner.SetBorder(false)
	t.spinner.SetBackgroundColor(tcell.ColorDefault)

	// Info line: session/branch info
	t.info = tview.NewTextView().SetDynamicColors(true)
	t.info.SetBorder(false)
	t.info.SetBackgroundColor(tcell.ColorDefault)

	// Footer: tokens/model
	t.footer = tview.NewTextView().SetDynamicColors(true)
	t.footer.SetBorder(false)
	t.footer.SetBackgroundColor(tcell.ColorDefault)

	// Separators using Box with custom draw (respects terminal width)
	sep1 := t.newSeparator()
	sep2 := t.newSeparator()

	// Palette overlay (hidden initially, 0 height)
	t.paletteTV = tview.NewTextView().SetDynamicColors(true)
	t.paletteTV.SetBorder(false)
	t.paletteTV.SetBackgroundColor(tcell.ColorDefault)

	t.spacerTop = tview.NewBox().SetBackgroundColor(tcell.ColorDefault)
	t.spacerBot = tview.NewBox().SetBackgroundColor(tcell.ColorDefault)

	// Layout (top-to-bottom):
	//   output   (flex)
	//   spinner  (1 line)
	//   ─────── sep1
	//   palette  (0..N lines, dynamic)
	//   input    (1 line)
	//   ─────── sep2
	//   info     (1 line)
	//   footer   (1 line)
	t.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(t.output, 0, 1, false).
		AddItem(t.spinner, 3, 0, false).
		AddItem(sep1, 1, 0, false).
		AddItem(t.inputTV, 1, 0, false).
		AddItem(sep2, 1, 0, false).
		AddItem(t.spacerTop, 0, 0, false).
		AddItem(t.paletteTV, 0, 0, false).
		AddItem(t.spacerBot, 0, 0, false).
		AddItem(t.info, 1, 0, false).
		AddItem(t.footer, 1, 0, false)

	t.app.SetRoot(t.flex, true).SetFocus(t.inputTV)
}

func (t *TUI) newSeparator() *tview.Box {
	return tview.NewBox().
		SetBackgroundColor(tcell.ColorDefault).
		SetDrawFunc(func(screen tcell.Screen, x, y, width, height int) (int, int, int, int) {
			style := tcell.StyleDefault.Foreground(tcell.NewHexColor(0x5fafd7)).Background(tcell.ColorDefault)
			for i := x; i < x+width; i++ {
				screen.SetContent(i, y, '─', nil, style)
			}
			return x, y, width, height
		})
}

// ── Palette data (dynamic from session) ─────────────────────────────────────────────

// loadSessionCommands fetches commands from the active session and caches them.
func (t *TUI) loadSessionCommands() {
	if t.sessionID == "" {
		return
	}
	cmds, err := t.client.ListCommands(t.sessionID)
	if err != nil {
		return
	}
	t.sessionCmds = cmds
}

// rootPaletteItems builds level-1 items: globals first, then session-scoped, then quit.
func (t *TUI) rootPaletteItems() []paletteItem {
	var items []paletteItem
	// Global commands
	items = append(items,
		paletteItem{name: "connect", desc: "Connect a provider"},
		paletteItem{name: "disconnect", desc: "Disconnect a provider"},
		paletteItem{name: "resume", desc: "Resume a previous session"},
		paletteItem{name: "delete", desc: "Delete a session"},
	)
	// Session-scoped commands from API
	for _, cmd := range t.sessionCmds {
		items = append(items, paletteItem{name: cmd.Name, desc: cmd.Description})
	}
	// Quit last
	items = append(items, paletteItem{name: "quit", desc: "Exit harness"})
	return items
}

// cmdType returns: "quit" | "none" | "list" | "list-free" | "free" | "optional"
// "list-free" = pick from list then type a value (connect: provider → api key)
func (t *TUI) cmdType(cmdName string) string {
	switch cmdName {
	case "quit":
		return "quit"
	case "connect":
		return "list-free" // pick provider → type api key (or auto for oauth)
	case "disconnect", "resume", "delete":
		return "list" // pick from dynamic list
	}
	for _, cmd := range t.sessionCmds {
		if cmd.Name != cmdName {
			continue
		}
		if len(cmd.Params) == 0 {
			return "none"
		}
		p := cmd.Params[0]
		if !p.Required {
			return "optional"
		}
		if len(p.Values) > 0 {
			return "list"
		}
		return "free"
	}
	return "none"
}

func (t *TUI) hasSubPalette(cmdName string) bool {
	return t.cmdType(cmdName) == "list"
}

// getSubItems returns level-2 palette items for a command.
func (t *TUI) providersInactive() []paletteItem {
	data, err := t.client.GetProviders()
	if err != nil {
		return nil
	}
	var providers []map[string]any
	json.Unmarshal(data, &providers)
	var items []paletteItem
	for _, p := range providers {
		if active, _ := p["active"].(bool); active {
			continue
		}
		name, _ := p["name"].(string)
		items = append(items, paletteItem{name: name, desc: "inactive"})
	}
	return items
}

func (t *TUI) providersActive() []paletteItem {
	data, err := t.client.GetProviders()
	if err != nil {
		return nil
	}
	var providers []map[string]any
	json.Unmarshal(data, &providers)
	var items []paletteItem
	for _, p := range providers {
		if active, _ := p["active"].(bool); !active {
			continue
		}
		name, _ := p["name"].(string)
		isSub, _ := p["is_subscription"].(bool)
		desc := ""
		if isSub {
			desc = "subscription"
		}
		items = append(items, paletteItem{name: name, desc: desc})
	}
	return items
}

func (t *TUI) sessionsForCWD(excludeActive bool) []paletteItem {
	cwd, _ := os.Getwd()
	data, err := t.client.ListSessionsByCWD(cwd)
	if err != nil {
		return nil
	}
	var sessions []map[string]any
	json.Unmarshal(data, &sessions)
	var items []paletteItem
	for _, s := range sessions {
		id, _ := s["id"].(string)
		if excludeActive && id == t.sessionID {
			continue
		}
		name, _ := s["name"].(string)
		if name == "" {
			name = id[:8]
		}
		sessCWD, _ := s["cwd"].(string)
		// name=session name (displayed left), desc=cwd (displayed right), id=internal
		items = append(items, paletteItem{name: name, desc: shortenPath(sessCWD), id: id})
	}
	return items
}

func (t *TUI) getSubItems(cmdName string) []paletteItem {
	switch cmdName {
	case "connect":
		return t.providersInactive()
	case "disconnect":
		return t.providersActive()
	case "resume":
		return t.sessionsForCWD(true)
	case "delete":
		return t.sessionsForCWD(true) // exclude active session
	}
	// Session-scoped commands with fixed values
	for _, cmd := range t.sessionCmds {
		if cmd.Name != cmdName {
			continue
		}
		for _, p := range cmd.Params {
			if len(p.Values) > 0 {
				var items []paletteItem
				for _, v := range p.Values {
					items = append(items, paletteItem{name: v})
				}
				return items
			}
		}
	}
	return nil
}

// renderPalette redraws paletteTV and resizes it in the flex.
func (t *TUI) renderPalette() {
	if !t.pal.open {
		t.flex.ResizeItem(t.spacerTop, 0, 0)
		t.flex.ResizeItem(t.paletteTV, 0, 0)
		t.flex.ResizeItem(t.spacerBot, 0, 0)
		t.paletteTV.Clear()
		return
	}

	items := t.pal.filtered()
	lv := t.pal.current()

	// Terminal width: use output rect (always rendered), cap desc conservatively
	_, _, termWidth, _ := t.output.GetInnerRect()
	if termWidth <= 0 {
		termWidth = 80
	}
	// Max desc chars: terminal width minus prefix " → name  " minus safety margin
	// Cap at 50 to keep descriptions readable and short
	const maxDescLen = 50

	const maxVisible = 5
	total := len(items)
	var lines []string

	if total == 0 {
		lines = append(lines, clrFooter+"No matches"+clrReset)
	} else {
		// Compute window
		start := lv.sel - maxVisible/2
		if start < 0 {
			start = 0
		}
		end := start + maxVisible
		if end > total {
			end = total
			start = end - maxVisible
			if start < 0 {
				start = 0
			}
		}

		// Max name width in window (for alignment)
		maxName := 0
		for i := start; i < end; i++ {
			if len(items[i].name) > maxName {
				maxName = len(items[i].name)
			}
		}

		for i := start; i < end; i++ {
			it := items[i]
			pad := strings.Repeat(" ", maxName-len(it.name)+2)
			// Truncate desc to fit in one line
			desc := it.desc
			// Available = termWidth - prefix(3) - name - pad - safety(1)
			avail := termWidth - 3 - maxName - 2 - 1
			if avail > maxDescLen {
				avail = maxDescLen
			}
			if avail < 0 {
				avail = 0
			}
			if len(desc) > avail {
				if avail > 3 {
					desc = desc[:avail-3] + "..."
				} else {
					desc = ""
				}
			}
			if i == lv.sel {
				lines = append(lines, fmt.Sprintf(clrUser+"→"+clrReset+" %s%s"+clrFooter+"%s"+clrReset, it.name, pad, desc))
			} else {
				lines = append(lines, fmt.Sprintf(clrFooter+"  %s%s%s"+clrReset, it.name, pad, desc))
			}
		}
		// Always show counter when total > maxVisible
		if total > maxVisible {
			lines = append(lines, fmt.Sprintf(clrFooter+"(%d/%d)"+clrReset, lv.sel+1, total))
		}
	}

	t.paletteTV.Clear()
	fmt.Fprint(t.paletteTV, strings.Join(lines, "\n"))
	t.flex.ResizeItem(t.spacerTop, 0, 0)
	t.flex.ResizeItem(t.paletteTV, len(lines), 0)
	t.flex.ResizeItem(t.spacerBot, 0, 0)
}

// paletteCommands kept for handleCommand compat
func paletteCommands() []string {
	return []string{"/model", "/thinking", "/compact", "/rename", "/quit"}
}

// ── Auto-connect ──────────────────────────────────────────────────────────

func (t *TUI) autoConnect() {
	// Load available models
	data, err := t.client.ListModels()
	if err != nil {
		t.showWarn("Failed to reach server. Is harness running?")
		return
	}
	var models []map[string]any
	json.Unmarshal(data, &models)
	if len(models) == 0 {
		t.showWarn("No active providers. Use /connect to add one.")
		return
	}

	// Build set of available model IDs
	available := map[string]bool{}
	for _, m := range models {
		if id, _ := m["model"].(string); id != "" {
			available[id] = true
		}
	}

	// Get settings
	var settingsModel, settingsThinking string
	if data, err = t.client.GetSettings(); err == nil {
		var settings map[string]any
		json.Unmarshal(data, &settings)
		settingsModel, _ = settings["active_model"].(string)
		settingsThinking, _ = settings["thinking_level"].(string)
	}

	// Resolve model: CLI flag > settings > first available
	if t.overrideModel != "" && available[t.overrideModel] {
		t.model = t.overrideModel
	} else if settingsModel != "" && available[settingsModel] {
		t.model = settingsModel
	} else {
		if settingsModel != "" {
			t.showWarn(fmt.Sprintf("Model '%s' is not available. Falling back to first active model.", settingsModel))
		}
		t.model, _ = models[0]["model"].(string)
	}
	t.thinking = settingsThinking
	// CLI flag overrides settings for thinking
	if t.overrideThinking != "" {
		t.thinking = t.overrideThinking
	}

	// Check if selected model is subscription-based
	for _, m := range models {
		if id, _ := m["model"].(string); id == t.model {
			t.isSubscription, _ = m["is_subscription"].(bool)
			break
		}
	}

	// Create or resume session
	cwd, _ := os.Getwd()
	var sess map[string]any

	if t.resumeID != "" {
		// Resume existing session
		t.appendLine(clrConfirm + "── resuming session ──" + clrReset + "\n\n")
		data, err = t.client.ResumeSession(t.resumeID)
		if err != nil {
			t.showWarn(fmt.Sprintf("Failed to resume session: %s", err.Error()))
			// Fall through to create new
		} else {
			json.Unmarshal(data, &sess)
			t.sessionID, _ = sess["id"].(string)
			t.sessionName, _ = sess["name"].(string)
			t.model, _ = sess["model"].(string)
			t.thinking = ""
			if th, _ := sess["thinking"].(string); th != "" {
				t.thinking = th
			}
			// Apply overrides to resumed session
			if t.overrideModel != "" && t.overrideModel != t.model {
				t.client.ExecCommand(t.sessionID, "model", map[string]any{"model": t.overrideModel}) //nolint
				t.model = t.overrideModel
			}
			if t.overrideThinking != "" && t.overrideThinking != t.thinking {
				t.client.ExecCommand(t.sessionID, "thinking", map[string]any{"level": t.overrideThinking}) //nolint
				t.thinking = t.overrideThinking
			}
			t.loadStatsFromSession(sess)
			t.loadSessionCommands()
			t.app.QueueUpdateDraw(func() {
				t.output.Clear()
				t.updateInfo()
			})
			t.renderHistory()
			return
		}
	}

	// Create new session (default path when no --resume or resume failed)
	data, err = t.client.CreateSession(t.model, cwd)
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to create session: %s", err.Error()))
		return
	}
	json.Unmarshal(data, &sess)
	t.sessionID, _ = sess["id"].(string)
	t.sessionName, _ = sess["name"].(string)
	if th, _ := sess["thinking"].(string); th != "" {
		t.thinking = th
	}
	t.loadStatsFromSession(sess)
	t.loadSessionCommands()
	t.app.QueueUpdateDraw(func() { t.updateInfo() })
}

// loadStatsFromSession reads the stats block from a session API response
// and populates the TUI footer state. Used for both new sessions and
// resumed sessions (same response shape from both endpoints).
//
// TODO: when resume session is implemented, call loadStatsFromSession
// with the resume response — stats will carry all accumulated values
// from the previous session (input, output, cache, cost, context_usage).
func (t *TUI) loadStatsFromSession(sess map[string]any) {
	stats, ok := sess["stats"].(map[string]any)
	if !ok {
		return
	}
	if v, _ := stats["input_tokens"].(float64); v > 0 {
		t.stats.input = int(v)
	}
	if v, _ := stats["output_tokens"].(float64); v > 0 {
		t.stats.output = int(v)
	}
	if v, _ := stats["cache_read"].(float64); v > 0 {
		t.stats.cacheRead = int(v)
	}
	if v, _ := stats["cache_write"].(float64); v > 0 {
		t.stats.cacheWrite = int(v)
	}
	if v, _ := stats["cost_usd"].(float64); v > 0 {
		t.stats.cost = v
	}
	if v, _ := stats["context_usage"].(float64); v > 0 {
		t.stats.contextPct = v
	}
	if v, _ := stats["context_window"].(float64); v > 0 {
		t.stats.contextWin = int(v)
	}
}

// renderHistory fetches and renders all messages from the active session.
func (t *TUI) renderHistory() {
	data, err := t.client.GetMessages(t.sessionID)
	if err != nil {
		return
	}
	var messages []map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		return
	}
	for _, msg := range messages {
		// Check compaction flag in meta
		if meta, ok := msg["meta"].(map[string]any); ok {
			if isCompaction, _ := meta["is_compaction"].(bool); isCompaction {
				// Extract summary from text part
				summary := ""
				if parts, ok := msg["parts"].([]any); ok && len(parts) > 0 {
					if p, ok := parts[0].(map[string]any); ok {
						const prefix = "Previous conversation summary:\n\n"
						full, _ := p["text"].(string)
						summary = strings.TrimPrefix(full, prefix)
					}
				}
				safe := tview.Escape(summarize(summary))
				t.appendLine("\n" + clrCompact + "[::b]\u27f3 Compact()[-:-:-]" + clrCompact + "(\n" + clrReset)
				t.appendLine(clrCompact + ")[-:-:-]\n")
				t.appendLine(fmt.Sprintf(clrToolOK+"✓"+clrReset+" [::d]%s[-:-:-]\n\n", safe))
				continue
			}
		}
		role, _ := msg["role"].(string)
		parts, _ := msg["parts"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			switch {
			case part["text"] != nil:
				text, _ := part["text"].(string)
				if text == "" {
					continue
				}
				if role == "user" {
					t.appendLine(clrUser + "❯ " + tview.Escape(text) + clrReset + "\n\n")
				} else {
					t.appendLine(tview.Escape(text) + "\n\n")
				}
			case part["tool_call"] != nil:
				tc, _ := part["tool_call"].(map[string]any)
				name, _ := tc["name"].(string)
				input, _ := tc["input"].(map[string]any)
				args := ""
				if b, err := json.Marshal(input); err == nil {
					args = strings.TrimPrefix(strings.TrimSuffix(string(b), "}"), "{")
				}
				t.appendLine(clrToolName + "[::b]⚙ " + tview.Escape(name) + "[-:-:-]" + clrToolName + "(" + clrReset)
				if args != "" {
					t.appendLine("[::d]" + tview.Escape(args) + "[-:-:-]")
				}
				t.appendLine(clrToolName + ")" + clrReset + "\n")
			case part["tool_result"] != nil:
				tr, _ := part["tool_result"].(map[string]any)
				isErr, _ := tr["is_error"].(bool)
				var output string
				if content, _ := tr["content"].([]any); len(content) > 0 {
					if c0, _ := content[0].(map[string]any); c0 != nil {
						output, _ = c0["text"].(string)
					}
				}
				safe := tview.Escape(summarize(output))
				if isErr {
					t.appendLine(fmt.Sprintf(clrToolErr+"✗"+clrReset+" [::d]%s[-:-:-]\n\n", safe))
				} else {
					t.appendLine(fmt.Sprintf(clrToolOK+"✓"+clrReset+" [::d]%s[-:-:-]\n\n", safe))
				}
			}
		}
	}
}

func (t *TUI) closeCurrentSession() {
	if t.sessionID == "" {
		return
	}
	if t.sseCancel != nil {
		t.sseCancel()
	}
	t.client.CloseSession(t.sessionID) //nolint
	t.sessionID = ""
}

// isSubscriptionProvider returns true if the provider is subscription/OAuth type.
func (t *TUI) isSubscriptionProvider(name string) bool {
	data, err := t.client.GetProviders()
	if err != nil {
		return false
	}
	var providers []map[string]any
	json.Unmarshal(data, &providers)
	for _, p := range providers {
		if n, _ := p["name"].(string); n == name {
			isSub, _ := p["is_subscription"].(bool)
			return isSub
		}
	}
	return false
}

// connectOAuthFlow runs the OAuth authentication flow for a provider.
func (t *TUI) connectOAuthFlow(provName string) {
	t.appendLine(clrConfirm + "Starting OAuth for " + provName + "..." + clrReset + "\n\n")
	t.app.Suspend(func() {
		creds, err := ObtainOAuthCredentials(provName)
		if err != nil {
			t.appendLine(fmt.Sprintf(clrError+"OAuth failed: %s"+clrReset+"\n\n", err.Error()))
			return
		}
		_, connErr := t.client.ConnectProviderWithCreds(provName, creds)
		if connErr != nil {
			t.appendLine(fmt.Sprintf(clrError+"connect failed: %s"+clrReset+"\n\n", connErr.Error()))
		} else {
			t.appendLine(fmt.Sprintf(clrConfirm+"connected: %s"+clrReset+"\n\n", provName))
			t.app.QueueUpdateDraw(func() { t.afterProviderChange() })
		}
	})
}

// afterProviderChange refreshes state after connect/disconnect:
// session commands (which include the model list for the model command).
// The palette sub-items re-fetch from API lazily, so they always show fresh data.
func (t *TUI) afterProviderChange() {
	t.loadSessionCommands()
	// If current model's provider was disconnected, clear it
	t.validateCurrentModel()
}

// validateCurrentModel checks if the current model's provider is still active.
// If not, clears the model so the next prompt fails early with a clear error.
func (t *TUI) validateCurrentModel() {
	if t.model == "" || t.sessionID == "" {
		return
	}
	data, err := t.client.ListModels()
	if err != nil {
		return
	}
	var models []map[string]any
	json.Unmarshal(data, &models)
	for _, m := range models {
		if id, _ := m["model"].(string); id == t.model {
			return // model still valid
		}
	}
	// Model no longer available
	t.appendLine(clrWarn + "⚠ Current model is no longer available. Select a new one with /model." + clrReset + "\n\n")
}

func (t *TUI) showWarn(msg string) {
	t.app.QueueUpdateDraw(func() {
		fmt.Fprintf(t.output, clrWarn+"⚠ %s"+clrReset+"\n\n", msg)
	})
}

// ── Custom input & palette ──────────────────────────────────────────────────

func (t *TUI) renderInput() {
	if t.inputBuf == "" {
		t.inputTV.SetText(clrPlaceholder + "Type a message or / for commands..." + clrReset)
	} else {
		t.inputTV.SetText(tview.Escape(t.inputBuf) + clrUser + "█" + clrReset)
	}
}

func (t *TUI) redraw() {
	t.renderInput()
	t.renderPalette()
}

func (t *TUI) handleKey(event *tcell.EventKey) *tcell.EventKey {
	// Always-on
	switch event.Key() {
	case tcell.KeyPgUp:
		t.output.InputHandler()(tcell.NewEventKey(tcell.KeyPgUp, 0, tcell.ModNone), nil)
		return nil
	case tcell.KeyPgDn:
		t.output.InputHandler()(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone), nil)
		return nil
	case tcell.KeyCtrlC, tcell.KeyCtrlD:
		t.closeCurrentSession()
		t.app.Stop()
		return nil
	}

	if t.pal.open {
		return t.handleKeyPalette(event)
	}
	return t.handleKeyNormal(event)
}

// ── Palette mode ────────────────────────────────────────────────────────────────

func (t *TUI) handleKeyPalette(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyUp:
		t.pal.moveUp()
		t.redraw()
		return nil

	case tcell.KeyDown:
		t.pal.moveDown()
		t.redraw()
		return nil

	case tcell.KeyEsc:
		if t.pal.popLevel() {
			t.inputBuf = "/"
			t.pal.setFilter("")
		} else {
			t.inputBuf = ""
			t.pal.close()
		}
		t.redraw()
		return nil

	case tcell.KeyTab:
		// Tab always autocompletes into input and closes palette — never executes
		sel, ok := t.pal.selected()
		if !ok {
			return nil
		}
		if t.pal.depth() == 1 {
			typ := t.cmdType(sel.name)
			switch typ {
			case "list", "list-free":
				subs := t.getSubItems(sel.name)
				t.pal.pushSub(sel.name, subs)
				t.inputBuf = ""
			case "free":
				t.inputBuf = "/" + sel.name + " "
				t.pal.close()
			case "none", "optional", "quit":
				t.inputBuf = "/" + sel.name
				t.pal.close()
			}
		} else {
			// Level 2: put /cmd value in input, close
			parentCmd := t.pal.current().parentCmd
			token := sel.name
			if sel.id != "" {
				token = sel.id
			}
			t.inputBuf = "/" + parentCmd + " " + token
			t.pal.close()
		}
		t.redraw()
		return nil

	case tcell.KeyEnter:
		sel, ok := t.pal.selected()
		if !ok {
			return nil
		}
		if t.pal.depth() == 1 {
			typ := t.cmdType(sel.name)
			switch typ {
			case "list", "list-free":
				subs := t.getSubItems(sel.name)
				t.pal.pushSub(sel.name, subs)
				t.inputBuf = ""
				t.redraw()
			case "free":
				t.inputBuf = "/" + sel.name + " "
				t.pal.close()
				t.redraw()
			case "none":
				cmd := "/" + sel.name
				t.inputBuf = ""
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			case "optional":
				cmd := "/" + sel.name
				t.inputBuf = ""
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			case "quit":
				t.closeCurrentSession()
				t.app.Stop()
			}
		} else {
			// Level 2: execute or prompt for API key
			parentCmd := t.pal.current().parentCmd
			// Use id if available (sessions), otherwise name (providers, values)
			token := sel.name
			if sel.id != "" {
				token = sel.id
			}
			if parentCmd == "connect" && sel.desc != "subscription" {
				// Non-subscription: need API key — put in input for typing
				t.inputBuf = "/connect " + token + " "
				t.pal.close()
				t.redraw()
			} else if parentCmd == "connect" {
				// Subscription: execute OAuth flow directly
				cmd := "/" + parentCmd + " " + token
				t.inputBuf = ""
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			} else {
				cmd := "/" + parentCmd + " " + token
				t.inputBuf = ""
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			}
		}
		return nil

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(t.inputBuf) > 0 {
			runes := []rune(t.inputBuf)
			t.inputBuf = string(runes[:len(runes)-1])
		}
		if t.inputBuf == "" {
			if !t.pal.popLevel() {
				t.pal.close()
			} else {
				t.inputBuf = "/"
				t.pal.setFilter("")
			}
		} else if t.pal.depth() == 1 {
			t.pal.setFilter(strings.TrimPrefix(t.inputBuf, "/"))
		} else {
			t.pal.setFilter(t.inputBuf)
		}
		t.redraw()
		return nil

	case tcell.KeyRune:
		t.inputBuf += string(event.Rune())
		if t.pal.depth() == 1 {
			t.pal.setFilter(strings.TrimPrefix(t.inputBuf, "/"))
		} else {
			t.pal.setFilter(t.inputBuf)
		}
		t.redraw()
		return nil
	}
	return nil
}

// ── Normal mode ────────────────────────────────────────────────────────────────

func (t *TUI) handleKeyNormal(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEsc:
		t.inputBuf = ""
		t.redraw()
		return nil

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(t.inputBuf) > 0 {
			runes := []rune(t.inputBuf)
			t.inputBuf = string(runes[:len(runes)-1])
		}
		// Reopen palette if still in command mode (no space = still on cmd word)
		if strings.HasPrefix(t.inputBuf, "/") {
			parts := strings.Fields(t.inputBuf)
			if len(parts) <= 1 && !strings.HasSuffix(t.inputBuf, " ") {
				if !t.pal.open {
					t.pal.openRoot(t.rootPaletteItems())
				}
				t.pal.setFilter(strings.TrimPrefix(t.inputBuf, "/"))
			}
		} else if t.inputBuf == "" {
			t.pal.close()
		}
		t.redraw()
		return nil

	case tcell.KeyEnter:
		text := strings.TrimSpace(t.inputBuf)
		if text == "" {
			return nil
		}
		if strings.HasPrefix(text, "/") {
			parts := strings.Fields(text)
			cmd := strings.TrimPrefix(parts[0], "/")
			typ := t.cmdType(cmd)
			switch typ {
			case "list", "list-free":
				if len(parts) < 2 {
					subs := t.getSubItems(cmd)
					t.pal.openRoot(t.rootPaletteItems())
					t.pal.pushSub(cmd, subs)
					t.inputBuf = ""
					t.redraw()
					return nil
				}
			case "free":
				if len(parts) < 2 {
					// Don't execute — keep in input, user must type value
					t.inputBuf = "/" + cmd + " "
					t.redraw()
					return nil
				}
			case "quit":
				t.app.Stop()
				return nil
			}
		}
		t.inputBuf = ""
		t.pal.close()
		t.redraw()
		go t.handleInput(text)
		return nil

	case tcell.KeyRune:
		t.inputBuf += string(event.Rune())
		if strings.HasPrefix(t.inputBuf, "/") {
			parts := strings.Fields(t.inputBuf)
			// Only show palette if we're still on the command word (no space yet)
			if len(parts) <= 1 && !strings.HasSuffix(t.inputBuf, " ") {
				if !t.pal.open {
					t.pal.openRoot(t.rootPaletteItems())
				}
				t.pal.setFilter(strings.TrimPrefix(t.inputBuf, "/"))
			} else {
				// Command already has param — close palette, let user type freely
				t.pal.close()
			}
		}
		t.redraw()
		return nil
	}
	return event
}

// ── Input ─────────────────────────────────────────────────────────────────

func (t *TUI) handleInput(text string) {
	if strings.HasPrefix(text, "/") {
		t.handleCommand(text)
		return
	}
	if t.sessionID == "" {
		t.appendLine(clrError + "No active session." + clrReset + "\n")
		return
	}

	data, err := t.client.SendPrompt(t.sessionID, text)
	if err != nil {
		t.appendLine(clrError + "Failed to send: " + err.Error() + clrReset + "\n\n")
		return
	}

	var resp map[string]string
	json.Unmarshal(data, &resp)
	status := resp["status"]

	if status == "queued" {
		// Prompt was queued — don't cancel SSE, don't show user message yet
		t.queueCount++
		t.localQueue = append(t.localQueue, tview.Escape(text))
		t.spinning = true
		// If no active SSE, start one (first prompt ever queued somehow? shouldn't happen, but defensive)
		if t.sseCancel == nil {
			ctx, cancel := context.WithCancel(context.Background())
			t.sseCancel = cancel
			go t.streamEvents(ctx)
		}
		t.app.QueueUpdateDraw(func() { t.updateInfo() })
		return
	}

	// Prompt started immediately — show user message and start streaming
	t.appendLine(clrUser + "❯ " + tview.Escape(text) + clrReset + "\n\n")

	// Clear any stale queue state (previous queued prompts processed by now)
	t.queueCount = 0
	t.localQueue = nil

	t.spinning = true
	if t.sseCancel != nil {
		t.sseCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.sseCancel = cancel
	go t.streamEvents(ctx)
}

func (t *TUI) handleCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}
	cmd := strings.TrimPrefix(parts[0], "/")

	if cmd == "quit" || cmd == "exit" {
		t.closeCurrentSession()
		t.app.Stop()
		return
	}

	// Global commands (don't require active session)
	switch cmd {
	case "connect":
		// /connect <provider> [api_key]
		if len(parts) < 2 {
			return
		}
		provName := parts[1]

		// Check if OAuth/subscription provider
		if t.isSubscriptionProvider(provName) {
			t.connectOAuthFlow(provName)
			return
		}

		// API key provider
		apiKey := ""
		if len(parts) > 2 {
			apiKey = strings.Join(parts[2:], " ")
		}
		_, err := t.client.ConnectProvider(provName, apiKey)
		if err != nil {
			t.appendLine(fmt.Sprintf(clrError+"connect failed: %s"+clrReset+"\n\n", err.Error()))
		} else {
			t.appendLine(fmt.Sprintf(clrConfirm+"connected: %s"+clrReset+"\n\n", provName))
			t.afterProviderChange()
		}
		return
	case "disconnect":
		if len(parts) < 2 {
			return
		}
		provName := parts[1]
		_, err := t.client.DisconnectProvider(provName)
		if err != nil {
			t.appendLine(fmt.Sprintf(clrError+"disconnect failed: %s"+clrReset+"\n\n", err.Error()))
		} else {
			t.appendLine(fmt.Sprintf(clrConfirm+"disconnected: %s"+clrReset+"\n\n", provName))
			t.afterProviderChange()
		}
		return
	case "resume":
		if len(parts) < 2 {
			return
		}
		sessID := parts[1]
		t.closeCurrentSession()
		data, err := t.client.ResumeSession(sessID)
		if err != nil {
			t.appendLine(fmt.Sprintf(clrError+"resume failed: %s"+clrReset+"\n\n", err.Error()))
			return
		}
		var sess map[string]any
		json.Unmarshal(data, &sess)
		t.sessionID, _ = sess["id"].(string)
		t.sessionName, _ = sess["name"].(string)
		if th, _ := sess["thinking"].(string); th != "" {
			t.thinking = th
		}
		t.loadStatsFromSession(sess)
		t.loadSessionCommands()
		t.app.QueueUpdateDraw(func() {
			t.output.Clear()
			t.updateInfo()
		})
		t.renderHistory()
		t.appendLine(fmt.Sprintf(clrConfirm+"── resumed: %s ──"+clrReset+"\n\n", t.sessionName))
		return
	case "delete":
		if len(parts) < 2 {
			return
		}
		sessID := parts[1]
		_, err := t.client.DeleteSession(sessID)
		if err != nil {
			t.appendLine(fmt.Sprintf(clrError+"delete failed: %s"+clrReset+"\n\n", err.Error()))
		} else {
			t.appendLine(clrConfirm + "session deleted" + clrReset + "\n\n")
		}
		return
	}

	if t.sessionID == "" {
		t.appendLine(clrError + "no active session" + clrReset + "\n\n")
		return
	}

	// Build params map based on command definition
	params := map[string]any{}
	var def *CommandDef
	for i := range t.sessionCmds {
		if t.sessionCmds[i].Name == cmd {
			def = &t.sessionCmds[i]
			break
		}
	}
	if def == nil {
		// sessionCmds may not be loaded yet — retry loading then try again
		t.loadSessionCommands()
		for i := range t.sessionCmds {
			if t.sessionCmds[i].Name == cmd {
				def = &t.sessionCmds[i]
				break
			}
		}
	}
	if def == nil {
		t.appendLine(fmt.Sprintf(clrError+"unknown command: %s"+clrReset+"\n\n", cmd))
		return
	}

	if len(def.Params) > 0 && len(parts) > 1 {
		paramName := def.Params[0].Name
		params[paramName] = strings.Join(parts[1:], " ")
	}

	// Execute via API
	_, err := t.client.ExecCommand(t.sessionID, cmd, params)
	if err != nil {
		t.appendLine(fmt.Sprintf(clrError+"%s"+clrReset+"\n\n", err.Error()))
		return
	}

	// Local side-effects (update state before rendering)
	switch cmd {
	case "model":
		if len(parts) > 1 {
			t.model = parts[1]
			// Refresh subscription flag
			if data, err := t.client.ListModels(); err == nil {
				var models []map[string]any
				json.Unmarshal(data, &models)
				for _, m := range models {
					if id, _ := m["model"].(string); id == t.model {
						t.isSubscription, _ = m["is_subscription"].(bool)
						break
					}
				}
			}
		}
	case "thinking":
		if len(parts) > 1 {
			t.thinking = parts[1]
		}
	case "rename":
		if len(parts) > 1 {
			t.sessionName = strings.Join(parts[1:], " ")
		}
	}

	// Confirmation + redraw info in one shot
	confirm := fmt.Sprintf(clrConfirm+"%s"+clrReset+"\n\n", strings.Join(parts, " "))
	t.app.QueueUpdateDraw(func() {
		fmt.Fprint(t.output, confirm)
		t.output.ScrollToEnd()
		t.updateInfo()
	})

	// Commands that trigger agent streaming
	if cmd == "compact" || cmd == "model" || strings.HasPrefix(cmd, "skill:") {
		if t.sseCancel != nil {
			t.sseCancel()
		}
		t.spinning = true
		ctx, cancel := context.WithCancel(context.Background())
		t.sseCancel = cancel
		go t.streamEvents(ctx)
	}
}

func (t *TUI) listModels() {
	data, _ := t.client.ListModels()
	var models []map[string]any
	json.Unmarshal(data, &models)
	t.appendLine(clrConfirm + "models:" + clrReset + "\n")
	for _, m := range models {
		model, _ := m["model"].(string)
		t.appendLine(clrConfirm + "  " + model + clrReset + "\n")
	}
	t.appendLine("\n")
}

// appendLine writes to the TextView safely from any goroutine (not the UI thread).
func (t *TUI) appendLine(s string) {
	t.app.QueueUpdateDraw(func() {
		fmt.Fprint(t.output, s)
		t.output.ScrollToEnd()
	})
}

// ── SSE streaming ─────────────────────────────────────────────────────────

func (t *TUI) streamEvents(ctx context.Context) {
	events, err := t.client.StreamEvents(ctx, t.sessionID)
	if err != nil {
		t.appendLine(fmt.Sprintf(clrError+"✗ %s"+clrReset+"\n\n", err.Error()))
		t.spinning = false
		return
	}

	// track where we are in output so we can add blank lines between blocks
	inThinking := false
	inText := false

	for {
		select {
		case <-ctx.Done():
			t.spinning = false
			return
		case evt, ok := <-events:
			if !ok {
				t.spinning = false
				return
			}
			typ, _ := evt["type"].(string)
			switch typ {

			case "thinking":
				inThinking = true
				delta, _ := evt["delta"].(string)
				t.appendLine(clrThinking + strings.ReplaceAll(delta, "[", "[[") + clrReset)

			case "text":
				// thinking block just ended
				if inThinking {
					t.appendLine("\n\n")
					inThinking = false
				}
				inText = true
				delta, _ := evt["delta"].(string)
				t.appendLine(strings.ReplaceAll(delta, "[", "[["))

			case "tool_start":
				// close any open block
				if inThinking || inText {
					t.appendLine("\n\n")
					inThinking = false
					inText = false
				}
				name, _ := evt["tool_name"].(string)
				// Bold amber tool name + opening paren
				t.appendLine(clrToolName + "[::b]⚙ " + tview.Escape(name) + "[-:-:-]" + clrToolName + "(" + clrReset)

			case "tool_args":
				delta, _ := evt["delta"].(string)
				// Strip outer braces, content in dim
				delta = strings.TrimPrefix(strings.TrimSpace(delta), "{")
				delta = strings.TrimSuffix(strings.TrimSpace(delta), "}")
				if delta != "" {
					t.appendLine("[::d]" + strings.ReplaceAll(delta, "[", "[[") + "[-:-:-]")
				}

			case "tool_call":
				// Close paren + newline before result
				t.appendLine(clrToolName + ")" + clrReset + "\n")

			case "tool_result":
				output, _ := evt["output"].(string)
				dur, _ := floatFromMap(evt, "duration")
				isErr, _ := evt["is_error"].(bool)
				safe := strings.ReplaceAll(summarize(output), "[", "[[")
				// result + trailing blank line (next block starts clean)
				if isErr {
					t.appendLine(fmt.Sprintf(clrToolErr+"✗"+clrReset+" [::d]%s (%.0fms)[-:-:-]\n\n", safe, dur))
				} else {
					t.appendLine(fmt.Sprintf(clrToolOK+"✓"+clrReset+" [::d]%s (%.0fms)[-:-:-]\n\n", safe, dur))
				}

			case "compact_start":
				t.compactStart = time.Now()
				t.appendLine(fmt.Sprintf("\n" + clrCompact + "[::b]⟳ Compact()[-:-:-]" + clrCompact + "(" + clrReset + "\n"))

			case "compact_end":
				dur := time.Since(t.compactStart).Milliseconds()
				summary, _ := evt["summary"].(string)
				safe := tview.Escape(summarize(summary))
				t.appendLine(fmt.Sprintf(clrCompact + ")[-:-:-]\n"))
				t.appendLine(fmt.Sprintf(clrToolOK+"✓"+clrReset+" [::d]%s (%dms)[-:-:-]\n\n", safe, dur))

			case "tokens":
				t.stats.input, _ = intFromMap(evt, "input")
				t.stats.output, _ = intFromMap(evt, "total_output")
				t.stats.cacheRead, _ = intFromMap(evt, "cache_read")
				t.stats.cacheWrite, _ = intFromMap(evt, "cache_write")
				t.stats.cost, _ = floatFromMap(evt, "cost_usd")
				t.stats.contextPct, _ = floatFromMap(evt, "context_usage")
				t.stats.contextWin, _ = intFromMap(evt, "context_window")
				t.app.QueueUpdateDraw(func() { t.updateInfo() })

			case "turn_end":
				// close last open block
				if inThinking || inText {
					t.appendLine("\n\n")
				}
				// Process queue: if there are pending prompts, show next one
				if t.queueCount > 0 && len(t.localQueue) > 0 {
					msg := t.localQueue[0]
					t.localQueue = t.localQueue[1:]
					t.queueCount--
					t.appendLine(clrUser + "❯ " + msg + clrReset + "\n\n")
					t.app.QueueUpdateDraw(func() { t.updateInfo() })
					// Reset streaming flags, keep going for next turn
					inThinking = false
					inText = false
					continue
				}
				t.spinning = false
				return

			case "error":
				msg, _ := evt["message"].(string)
				t.appendLine(fmt.Sprintf(clrError+"✗ %s"+clrReset+"\n\n", msg))
				t.spinning = false
				return
			}
		}
	}
}

// ── Spinner ───────────────────────────────────────────────────────────────

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var spinnerLabels = []string{"Boostaffing", "Maskarizing", "Outworlding", "Khanifying", "Emeraldizing"}

func (t *TUI) spinnerLoop() {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	frame := 0
	for {
		select {
		case <-t.spinnerStop:
			return
		case <-ticker.C:
			if !t.spinning {
				t.app.QueueUpdateDraw(func() {
					t.spinner.SetText("\n\n")
				})
				continue
			}
			f := spinnerFrames[frame%len(spinnerFrames)]
			lbl := spinnerLabels[(frame/10)%len(spinnerLabels)]
			frame++
			t.app.QueueUpdateDraw(func() {
				t.spinner.SetText(fmt.Sprintf("\n"+clrFooter+"%s %s..."+clrReset+"\n", f, lbl))
			})
		}
	}
}

// ── Info / Footer ─────────────────────────────────────────────────────────

func (t *TUI) updateInfo() {
	// info line: cwd (branch) • session name
	cwd, _ := os.Getwd()
	branch := gitBranch(cwd)
	loc := shortenPath(cwd)
	if branch != "" {
		loc += " (" + branch + ")"
	}
	name := t.sessionName
	if name == "" {
		name = "No session"
	}
	queue := ""
	if t.queueCount > 0 {
		queue = fmt.Sprintf(" [%d queued]", t.queueCount)
	}
	t.info.SetText(fmt.Sprintf(clrFooter+"%s • %s%s"+clrReset, loc, name, queue))

	// footer: tokens + model
	if t.model == "" {
		t.footer.SetText("")
		return
	}
	thinking := ""
	if t.thinking != "" && t.thinking != "off" {
		thinking = " • " + t.thinking
	}
	cache := ""
	if t.stats.cacheRead > 0 || t.stats.cacheWrite > 0 {
		cache = fmt.Sprintf(" R%s W%s", compactNum(t.stats.cacheRead), compactNum(t.stats.cacheWrite))
	}
	price := fmt.Sprintf("$%.3f", t.stats.cost)
	if t.isSubscription {
		price += " (sub)"
	}
	t.footer.SetText(fmt.Sprintf(
		clrFooter+"↑%s ↓%s%s %s %.1f%%/%s %s%s"+clrReset,
		compactNum(t.stats.input),
		compactNum(t.stats.output),
		cache,
		price,
		t.stats.contextPct*100,
		compactNum(t.stats.contextWin),
		t.model,
		thinking,
	))
}

// ── Helpers ───────────────────────────────────────────────────────────────

func summarize(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

func compactNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func gitBranch(cwd string) string {
	data, err := os.ReadFile(cwd + "/.git/HEAD")
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(data))
	if strings.HasPrefix(ref, "ref: refs/heads/") {
		return strings.TrimPrefix(ref, "ref: refs/heads/")
	}
	return ""
}

// shortenPath replaces the user's home directory with ~
func shortenPath(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func intFromMap(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func floatFromMap(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	if f, ok := v.(float64); ok {
		return f, true
	}
	return 0, false
}
