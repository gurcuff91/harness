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
	goclip "golang.design/x/clipboard"
)

// ── Color palette ──────────────────────────────────────────────────────────
const (
	clrPrimary = "[#26A69A]" // teal 400   — user input, separators  (Kaiban Teal dark-adapted)
	clrAccent  = "[#C8D96A]" // chartreuse — tool names, compact    (Kaiban Energy)
	clrOK      = "[#C8D96A]" // chartreuse — success                (Kaiban Energy)
	clrErr     = "[#D94068]" // rose       — errors                 (Kaiban Rose)
	clrWarn    = "[#B44CA0]" // violet     — warnings, stopped      (Kaiban Violet)
	clrDim     = "[::d]"     // dim        — thinking, footer, args
	clrReset   = "[-:-:-]"   // reset

	// Hex-only variants for use inside tview compound tags e.g. [#hex::i]
	clrPrimaryHex = "#26A69A"
	clrAccentHex  = "#C8D96A"

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
	agent            *agent.Agent
	client           *Client
	addr             string
	sessionID        string
	sessionName      string
	model            string
	overrideModel    string             // from --model flag (takes priority over settings)
	overrideThinking string             // from --thinking flag
	resumeID         string             // from --resume flag (resume instead of create)
	sseCancelFn      context.CancelFunc // persistent SSE, cancelled on quit
	lastSessionID    string             // for resume hint on exit

	// tview
	app       *tview.Application
	output    *tview.TextView
	spinner   *tview.TextView
	inputTV   *tview.TextView
	paletteTV *tview.TextView
	spacerTop *tview.Box
	spacerBot *tview.Box
	flex      *pasteableFlex
	info      *tview.TextView
	footer    *tview.TextView

	// input state
	inputBuf    string
	cursorPos   int // rune index into inputBuf
	inputHeight int // current rendered height of input (1-5)
	pal      palette

	// session state
	thinking       string
	isSubscription bool
	sessionCmds    []CommandDef
	readMode       bool   // reserved
	lastTurnText   strings.Builder // accumulates agent text during a turn for Ctrl+Y copy
	md             *mdState        // markdown streaming state (reset each turn)

	// state
	spinning     bool
	spinnerStop  chan struct{}
	compactStart time.Time
	queueCount   int             // number of prompts waiting in the session queue
	localQueue   []string        // pending user messages (for display)
	outputBuf    strings.Builder // full output content (only touched from uiOps goroutine)
	toolSlots    map[string]int  // toolID → 1 if slot reserved
	uiOps        chan func()     // serialized UI operations (written from stream goroutine, drained by uiOps goroutine)
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

	// uiOps: serialized channel for all outputBuf mutations.
	// The drain goroutine executes each op, then does ONE QueueUpdateDraw.
	t.uiOps = make(chan func(), 4096)
	go t.drainUIops(ctx)

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

	// Sync screen after every draw to prevent ghost chars from wide/emoji chars
	t.app.SetAfterDrawFunc(func(screen tcell.Screen) {
		screen.Sync()
	})

	err = t.app.EnableMouse(false).EnablePaste(true).Run()

	if t.lastSessionID != "" {
		fmt.Printf("\n  Resume: harness --resume %s\n\n", t.lastSessionID)
	}

	return err
}

// ── Layout ────────────────────────────────────────────────────────────────

func (t *TUI) buildUI() {
	// Output: scrollable, dynamic colors, tracks end
	t.output = tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(true).
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
	t.inputTV = tview.NewTextView()
	t.inputTV.SetDynamicColors(true).
		SetScrollable(true).
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
	t.flex = newPasteableFlex(func(text string) {
		// Normalize line endings from clipboard (\r\n -> \n, standalone \r -> \n)
		text = strings.ReplaceAll(text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		runes := []rune(t.inputBuf)
		paste := []rune(text)
		newRunes := make([]rune, len(runes)+len(paste))
		copy(newRunes, runes[:t.cursorPos])
		copy(newRunes[t.cursorPos:], paste)
		copy(newRunes[t.cursorPos+len(paste):], runes[t.cursorPos:])
		t.inputBuf = string(newRunes)
		t.cursorPos += len(paste)
		// Force resize by resetting inputHeight so renderInput always calls ResizeItem
		t.inputHeight = 0
		t.renderInput()
		// Schedule a second render from outside the event loop — some terminals
		// send EventResize after paste which redraws before our
		// ResizeItem takes effect. The queued draw guarantees the correct size.
		go t.app.QueueUpdateDraw(func() {
			t.inputHeight = 0
			t.renderInput()
		})
	})
	t.flex.SetDirection(tview.FlexRow).
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

// primaryColor parses clrPrimary hex for use with tcell directly.
func primaryColor() tcell.Color {
	// clrPrimary = "[#RRGGBB]" — extract hex
	s := clrPrimary // e.g. "[#29B6F6]"
	if len(s) == 9 && s[0] == '[' && s[1] == '#' {
		var r, g, b int32
		fmt.Sscanf(s[2:8], "%02x%02x%02x", &r, &g, &b)
		return tcell.NewRGBColor(r, g, b)
	}
	return tcell.ColorWhite
}

func (t *TUI) newSeparator() *tview.Box {
	return tview.NewBox().
		SetBackgroundColor(tcell.ColorDefault).
		SetDrawFunc(func(screen tcell.Screen, x, y, width, height int) (int, int, int, int) {
			style := tcell.StyleDefault.Foreground(primaryColor()).Background(tcell.ColorDefault)
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
		lines = append(lines, clrDim+"No matches"+clrReset)
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
				lines = append(lines, fmt.Sprintf(clrPrimary+"→"+clrReset+" %s%s"+clrDim+"%s"+clrReset, it.name, pad, desc))
			} else {
				lines = append(lines, fmt.Sprintf(clrDim+"  %s%s%s"+clrReset, it.name, pad, desc))
			}
		}
		// Always show counter when total > maxVisible
		if total > maxVisible {
			lines = append(lines, fmt.Sprintf(clrDim+"(%d/%d)"+clrReset, lv.sel+1, total))
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
		t.appendLine(clrDim + "── resuming session ──" + clrReset + "\n\n")
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
			t.startSSE()
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
	t.startSSE()
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
		// Compaction marker
		if meta, ok := msg["meta"].(map[string]any); ok {
			if isCompaction, _ := meta["is_compaction"].(bool); isCompaction {
				t.appendLine("\n" + clrAccent + "[::b]◎ Compacting[-:-:-]" + clrReset + "\n")
				t.appendLine(clrOK + "✔" + clrReset + " " + clrDim + "(history)" + clrReset + "\n\n")
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
					t.appendLine(clrPrimary + "❯ " + tview.Escape(text) + clrReset + "\n\n")
				} else {
					t.appendLine(tview.Escape(text) + "\n\n")
				}
			case part["tool_call"] != nil:
				tc, _ := part["tool_call"].(map[string]any)
				name, _ := tc["name"].(string)
				input, _ := tc["input"].(map[string]any)
				// Fix 1: marshal input to JSON then unescape unicode (e.g. \u0026 -> &)
				args := ""
				if b, err := json.Marshal(input); err == nil {
					raw := string(b)
					// Unescape unicode escapes so & renders as & not \u0026
					var unescaped string
					if err2 := json.Unmarshal([]byte(`"` + strings.ReplaceAll(raw[1:len(raw)-1], `"`, `\"`) + `"`), &unescaped); err2 == nil {
						args = strings.TrimPrefix(strings.TrimSuffix(unescaped, "}"), "{")
					} else {
						args = strings.TrimPrefix(strings.TrimSuffix(raw, "}"), "{")
					}
				}
				tClr2, tIco2 := toolStyle(name)
				// Render as single atomic block matching live style
				header := tClr2 + "[::b]" + tIco2 + " " + tview.Escape(name) + "[-:-:-]" + tClr2 + "(" + clrReset +
					clrDim + tview.Escape(args) + clrReset +
					tClr2 + ")" + clrReset + "\n"
				t.appendLine(header)
			case part["tool_result"] != nil:
				tr, _ := part["tool_result"].(map[string]any)
				isErr, _ := tr["is_error"].(bool)
				// Field is "output" (string), not content[]
				output, _ := tr["output"].(string)
				if output == "" {
					// fallback: Anthropic-style content[0].text
					if content, _ := tr["content"].([]any); len(content) > 0 {
						if c0, _ := content[0].(map[string]any); c0 != nil {
							output, _ = c0["text"].(string)
						}
					}
				}
				if isErr {
					clean := stripANSI(strings.TrimSpace(output))
					lines := strings.Split(clean, "\n")
					first := tview.Escape(strings.TrimSpace(lines[0]))
					t.appendLine(clrErr + "✘" + clrReset + " " + clrDim + first + clrReset + "\n\n")
				} else {
					lineCount := len(strings.Split(strings.TrimRight(output, "\n"), "\n"))
					if lineCount == 1 && strings.TrimSpace(output) == "" {
						lineCount = 0
					}
					t.appendLine(fmt.Sprintf(clrOK+"✔"+clrReset+" "+clrDim+"(%d lines)"+clrReset+"\n\n", lineCount))
				}
			}
		}
	}
}

func (t *TUI) closeCurrentSession() {
	if t.sessionID == "" {
		return
	}
	t.lastSessionID = t.sessionID
	t.client.CloseSession(t.sessionID) //nolint
	t.sessionID = ""
	if t.sseCancelFn != nil {
		t.sseCancelFn()
		t.sseCancelFn = nil
	}
}

// startSSE opens a persistent SSE connection that stays alive until quit.
func (t *TUI) startSSE() {
	if t.sessionID == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.sseCancelFn = cancel
	go t.streamEvents(ctx)
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
	t.appendLine(clrDim + "Starting OAuth for " + provName + "..." + clrReset + "\n\n")
	t.app.Suspend(func() {
		creds, err := ObtainOAuthCredentials(provName)
		if err != nil {
			t.appendLine(fmt.Sprintf(clrErr+"OAuth failed: %s"+clrReset+"\n\n", err.Error()))
			return
		}
		_, connErr := t.client.ConnectProviderWithCreds(provName, creds)
		if connErr != nil {
			t.appendLine(fmt.Sprintf(clrErr+"connect failed: %s"+clrReset+"\n\n", connErr.Error()))
		} else {
			t.appendLine(fmt.Sprintf(clrDim+"connected: %s"+clrReset+"\n\n", provName))
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
	t.appendLine(fmt.Sprintf(clrWarn+"⚠ %s"+clrReset+"\n\n", msg))
}

// ── Custom input & palette ──────────────────────────────────────────────────

func (t *TUI) renderInput() {
	newlines := 1
	if t.inputBuf == "" {
		t.inputTV.SetText(clrDim + "Type a message or / for commands..." + clrReset)
	} else {
		runes := []rune(t.inputBuf)
		pos := t.cursorPos
		if pos < 0 { pos = 0 }
		if pos > len(runes) { pos = len(runes) }
		before := tview.Escape(string(runes[:pos]))
		after  := tview.Escape(string(runes[pos:]))
		t.inputTV.SetText(before + clrPrimary + "█" + clrReset + after)
		newlines = strings.Count(t.inputBuf, "\n") + 1
		if newlines > 5 { newlines = 5 }
		cursorLine := strings.Count(string(runes[:pos]), "\n")
		t.inputTV.ScrollTo(cursorLine, 0)
	}
	if t.flex != nil && newlines != t.inputHeight {
		t.inputHeight = newlines
		t.flex.ResizeItem(t.inputTV, newlines, 0)
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
			t.inputBuf = "/"; t.cursorPos = len([]rune(t.inputBuf))
			t.pal.setFilter("")
		} else {
			t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
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
				t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
			case "free":
				t.inputBuf = "/" + sel.name + " "; t.cursorPos = len([]rune(t.inputBuf))
				t.pal.close()
			case "none", "optional", "quit":
				t.inputBuf = "/" + sel.name; t.cursorPos = len([]rune(t.inputBuf))
				t.pal.close()
			}
		} else {
			// Level 2: put /cmd value in input, close
			parentCmd := t.pal.current().parentCmd
			token := sel.name
			if sel.id != "" {
				token = sel.id
			}
			t.inputBuf = "/" + parentCmd + " " + token; t.cursorPos = len([]rune(t.inputBuf))
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
				t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
				t.redraw()
			case "free":
				t.inputBuf = "/" + sel.name + " "; t.cursorPos = len([]rune(t.inputBuf))
				t.pal.close()
				t.redraw()
			case "none":
				cmd := "/" + sel.name
				t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			case "optional":
				cmd := "/" + sel.name
				t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
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
				t.inputBuf = "/connect " + token + " "; t.cursorPos = len([]rune(t.inputBuf))
				t.pal.close()
				t.redraw()
			} else if parentCmd == "connect" {
				// Subscription: execute OAuth flow directly
				cmd := "/" + parentCmd + " " + token
				t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			} else {
				cmd := "/" + parentCmd + " " + token
				t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			}
		}
		return nil

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(t.inputBuf) > 0 && t.cursorPos > 0 {
			runes := []rune(t.inputBuf)
			t.inputBuf = string(runes[:t.cursorPos-1]) + string(runes[t.cursorPos:])
			t.cursorPos--
		}
		if t.inputBuf == "" {
			if !t.pal.popLevel() {
				t.pal.close()
			} else {
				t.inputBuf = "/"; t.cursorPos = len([]rune(t.inputBuf))
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
		runes := []rune(t.inputBuf)
		newRunes := make([]rune, len(runes)+1)
		copy(newRunes, runes[:t.cursorPos])
		newRunes[t.cursorPos] = event.Rune()
		copy(newRunes[t.cursorPos+1:], runes[t.cursorPos:])
		t.inputBuf = string(newRunes)
		t.cursorPos++
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
		if t.spinning && t.sessionID != "" {
			go t.client.StopSession(t.sessionID) //nolint
		}
		t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
		t.cursorPos = 0
		t.redraw()
		return nil

	case tcell.KeyLeft:
		if t.cursorPos > 0 { t.cursorPos-- }
		t.redraw()
		return nil

	case tcell.KeyRight:
		if t.cursorPos < len([]rune(t.inputBuf)) { t.cursorPos++ }
		t.redraw()
		return nil

	case tcell.KeyHome, tcell.KeyCtrlA:
		t.cursorPos = 0
		t.redraw()
		return nil

	case tcell.KeyEnd, tcell.KeyCtrlE:
		t.cursorPos = len([]rune(t.inputBuf))
		t.redraw()
		return nil

	case tcell.KeyCtrlY: // copy last agent response to clipboard
		text := strings.TrimSpace(t.lastTurnText.String())
		if text == "" {
			t.appendLine(clrWarn + "⚠ nothing to copy" + clrReset + "\n")
			return nil
		}
		goclip.Write(goclip.FmtText, []byte(text))
		t.appendLine(clrDim + "✔ copied to clipboard" + clrReset + "\n")
		return nil

	case tcell.KeyCtrlI: // Ctrl+I — paste image from clipboard
		go func() {
			path, err := PasteImageFromClipboard()
			if err != nil {
				t.app.QueueUpdateDraw(func() {
					t.appendLine(clrWarn + "⚠ clipboard image: " + err.Error() + clrReset + "\n")
				})
				return
			}
			if path == "" {
				t.app.QueueUpdateDraw(func() {
					t.appendLine(clrWarn + "⚠ no image in clipboard" + clrReset + "\n")
				})
				return
			}
			// Insert path at cursor position
			t.app.QueueUpdateDraw(func() {
				runes := []rune(t.inputBuf)
				paste := []rune(path)
				if len(runes) > 0 && t.cursorPos > 0 {
					paste = append([]rune(" "), paste...)
				}
				newRunes := make([]rune, len(runes)+len(paste))
				copy(newRunes, runes[:t.cursorPos])
				copy(newRunes[t.cursorPos:], paste)
				copy(newRunes[t.cursorPos+len(paste):], runes[t.cursorPos:])
				t.inputBuf = string(newRunes)
				t.cursorPos += len(paste)
				t.inputHeight = 0
				t.renderInput()
			})
		}()
		return nil

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(t.inputBuf) > 0 && t.cursorPos > 0 {
			runes := []rune(t.inputBuf)
			t.inputBuf = string(runes[:t.cursorPos-1]) + string(runes[t.cursorPos:])
			t.cursorPos--
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

	case tcell.KeyUp:
		// If input has no newlines → scroll output up 3 lines
		if !strings.Contains(t.inputBuf, "\n") {
			row, col := t.output.GetScrollOffset()
			t.output.ScrollTo(row-3, col)
			return nil
		}
		// Otherwise move cursor to previous logical line
		runes := []rune(t.inputBuf)
		// Find start of current line
		lineStart := t.cursorPos
		for lineStart > 0 && runes[lineStart-1] != '\n' { lineStart-- }
		if lineStart == 0 { break } // already on first line
		col := t.cursorPos - lineStart
		// Find start of previous line
		prevEnd := lineStart - 1 // the \n
		prevStart := prevEnd
		for prevStart > 0 && runes[prevStart-1] != '\n' { prevStart-- }
		prevLen := prevEnd - prevStart
		if col > prevLen { col = prevLen }
		t.cursorPos = prevStart + col
		t.redraw()
		return nil

	case tcell.KeyDown:
		// If input has no newlines → scroll output down 3 lines
		if !strings.Contains(t.inputBuf, "\n") {
			row, col := t.output.GetScrollOffset()
			t.output.ScrollTo(row+3, col)
			return nil
		}
		// Otherwise move cursor to next logical line
		runes := []rune(t.inputBuf)
		// Find end of current line
		lineStart := t.cursorPos
		for lineStart > 0 && runes[lineStart-1] != '\n' { lineStart-- }
		col := t.cursorPos - lineStart
		// Find start of next line
		nextStart := t.cursorPos
		for nextStart < len(runes) && runes[nextStart] != '\n' { nextStart++ }
		if nextStart >= len(runes) { break } // already on last line
		nextStart++ // skip the \n
		nextEnd := nextStart
		for nextEnd < len(runes) && runes[nextEnd] != '\n' { nextEnd++ }
		nextLen := nextEnd - nextStart
		if col > nextLen { col = nextLen }
		t.cursorPos = nextStart + col
		t.redraw()
		return nil

	case tcell.KeyCtrlJ: // Ctrl+J = newline
		runes := []rune(t.inputBuf)
		newRunes := make([]rune, len(runes)+1)
		copy(newRunes, runes[:t.cursorPos])
		newRunes[t.cursorPos] = '\n'
		copy(newRunes[t.cursorPos+1:], runes[t.cursorPos:])
		t.inputBuf = string(newRunes)
		t.cursorPos++
		t.redraw()
		return nil

	case tcell.KeyEnter:
		// Shift+Enter = newline (tcell reports Shift+Enter as ModShift)
		if event.Modifiers()&tcell.ModShift != 0 {
			runes := []rune(t.inputBuf)
			newRunes := make([]rune, len(runes)+1)
			copy(newRunes, runes[:t.cursorPos])
			newRunes[t.cursorPos] = '\n'
			copy(newRunes[t.cursorPos+1:], runes[t.cursorPos:])
			t.inputBuf = string(newRunes)
			t.cursorPos++
			t.redraw()
			return nil
		}
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
					t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
					t.redraw()
					return nil
				}
			case "free":
				if len(parts) < 2 {
					// Don't execute — keep in input, user must type value
					t.inputBuf = "/" + cmd + " "; t.cursorPos = len([]rune(t.inputBuf))
					t.redraw()
					return nil
				}
			case "quit":
				t.app.Stop()
				return nil
			}
		}
		t.inputBuf = ""; t.cursorPos = 0; t.inputHeight = 0
		t.pal.close()
		t.redraw()
		go t.handleInput(text)
		return nil

	case tcell.KeyRune:
		runes := []rune(t.inputBuf)
		newRunes := make([]rune, len(runes)+1)
		copy(newRunes, runes[:t.cursorPos])
		newRunes[t.cursorPos] = event.Rune()
		copy(newRunes[t.cursorPos+1:], runes[t.cursorPos:])
		t.inputBuf = string(newRunes)
		t.cursorPos++
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
		t.appendLine(clrErr + "No active session." + clrReset + "\n")
		return
	}

	data, err := t.client.SendPrompt(t.sessionID, text)
	if err != nil {
		t.appendLine(clrErr + "Failed to send: " + err.Error() + clrReset + "\n\n")
		return
	}

	var resp map[string]string
	json.Unmarshal(data, &resp)
	status := resp["status"]

	if status == "queued" {
		// Prompt queued — SSE is persistent, just track for display
		t.queueCount++
		t.localQueue = append(t.localQueue, tview.Escape(text))
		t.spinning = true
		t.app.QueueUpdateDraw(func() { t.updateInfo() })
		return
	}

	// Prompt started immediately — show user message
	t.appendLine(clrPrimary + "❯ " + tview.Escape(text) + clrReset + "\n\n")

	t.queueCount = 0
	t.localQueue = nil
	t.spinning = true
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
			t.appendLine(fmt.Sprintf(clrErr+"connect failed: %s"+clrReset+"\n\n", err.Error()))
		} else {
			t.appendLine(fmt.Sprintf(clrDim+"connected: %s"+clrReset+"\n\n", provName))
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
			t.appendLine(fmt.Sprintf(clrErr+"disconnect failed: %s"+clrReset+"\n\n", err.Error()))
		} else {
			t.appendLine(fmt.Sprintf(clrDim+"disconnected: %s"+clrReset+"\n\n", provName))
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
			t.appendLine(fmt.Sprintf(clrErr+"resume failed: %s"+clrReset+"\n\n", err.Error()))
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
		t.startSSE()
		t.appendLine(fmt.Sprintf(clrDim+"── resumed: %s ──"+clrReset+"\n\n", t.sessionName))
		return
	case "delete":
		if len(parts) < 2 {
			return
		}
		sessID := parts[1]
		_, err := t.client.DeleteSession(sessID)
		if err != nil {
			t.appendLine(fmt.Sprintf(clrErr+"delete failed: %s"+clrReset+"\n\n", err.Error()))
		} else {
			t.appendLine(clrDim + "session deleted" + clrReset + "\n\n")
		}
		return
	}

	if t.sessionID == "" {
		t.appendLine(clrErr + "no active session" + clrReset + "\n\n")
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
		t.appendLine(fmt.Sprintf(clrErr+"unknown command: %s"+clrReset+"\n\n", cmd))
		return
	}

	if len(def.Params) > 0 && len(parts) > 1 {
		paramName := def.Params[0].Name
		params[paramName] = strings.Join(parts[1:], " ")
	}

	// Execute via API
	_, err := t.client.ExecCommand(t.sessionID, cmd, params)
	if err != nil {
		t.appendLine(fmt.Sprintf(clrErr+"%s"+clrReset+"\n\n", err.Error()))
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

	// Confirmation — route through uiOps for ordering, then update info
	confirm := fmt.Sprintf(clrDim+"%s"+clrReset+"\n\n", strings.Join(parts, " "))
	t.appendLine(confirm)
	t.app.QueueUpdateDraw(func() { t.updateInfo() })

	// Commands that trigger agent streaming
	if cmd == "compact" || strings.HasPrefix(cmd, "skill:") {
		t.spinning = true
	}
}

func (t *TUI) listModels() {
	data, _ := t.client.ListModels()
	var models []map[string]any
	json.Unmarshal(data, &models)
	t.appendLine(clrDim + "models:" + clrReset + "\n")
	for _, m := range models {
		model, _ := m["model"].(string)
		t.appendLine(clrDim + "  " + model + clrReset + "\n")
	}
	t.appendLine("\n")
}

// drainUIops runs in its own goroutine and serializes all outputBuf mutations.
// Each op mutates outputBuf (no lock needed — single goroutine), then one QueueUpdateDraw refreshes tview.
func (t *TUI) drainUIops(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case op, ok := <-t.uiOps:
			if !ok {
				return
			}
			op()
			snap := t.outputBuf.String()
			t.app.QueueUpdateDraw(func() {
				t.output.SetText(snap)
				t.output.ScrollToEnd()
			})
		}
	}
}

// appendLine enqueues a text append. Safe to call from any goroutine.
func (t *TUI) appendLine(s string) {
	t.uiOps <- func() { t.outputBuf.WriteString(s) }
}

// reserveSlot appends the ⧖ Executing... placeholder inside uiOps (serialized).
// toolSlots tracking is also done inside the op so it's always consistent with outputBuf.
func (t *TUI) reserveSlot(toolID, toolName string) {
	tClr, _ := toolStyle(toolName)
	placeholder := `["` + toolID + `"]` + tClr + "\u29d6" + clrReset + clrDim + " Executing..." + clrReset + `[""]`
	t.uiOps <- func() {
		if t.toolSlots == nil {
			t.toolSlots = make(map[string]int)
		}
		t.toolSlots[toolID] = 1
		t.outputBuf.WriteString(placeholder + "\n\n")
	}
}

// fillSlot replaces the Executing... placeholder with result, serialized inside uiOps.
func (t *TUI) fillSlot(toolID, toolName, result string) {
	tClr, _ := toolStyle(toolName)
	placeholder := `["` + toolID + `"]` + tClr + "\u29d6" + clrReset + clrDim + " Executing..." + clrReset + `[""]` + "\n\n"
	t.uiOps <- func() {
		if t.toolSlots == nil || t.toolSlots[toolID] == 0 {
			t.outputBuf.WriteString(result)
			return
		}
		delete(t.toolSlots, toolID)
		old := t.outputBuf.String()
		newContent := strings.Replace(old, placeholder, result, 1)
		if newContent == old {
			newContent = old + result
		}
		t.outputBuf.Reset()
		t.outputBuf.WriteString(newContent)
	}
}

// ── SSE streaming ─────────────────────────────────────────────────────────

func (t *TUI) streamEvents(ctx context.Context) {
	events, err := t.client.StreamEvents(ctx, t.sessionID)
	if err != nil {
		t.appendLine(fmt.Sprintf(clrErr+"✘ %s"+clrReset+"\n\n", err.Error()))
		t.spinning = false
		return
	}

	// track where we are in output so we can add blank lines between blocks
	inThinking := false
	inText := false
	toolColors := make(map[string]string) // toolID → color
	toolIcons := make(map[string]string)  // toolID → icon
	toolNames := make(map[string]string)  // toolID → name
	argBufs := make(map[string]string)    // toolID → accumulated args so far
	var curToolClr = clrAccent

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

			case "turn_start":
				t.lastTurnText.Reset()
				t.md = newMdState()

			case "thinking":
				inThinking = true
				delta, _ := evt["delta"].(string)
				t.appendLine(clrDim + strings.ReplaceAll(delta, "[", "[[") + clrReset)

			case "text":
				if inThinking {
					t.appendLine("\n\n") // close thinking block
					inThinking = false
				}
				inText = true
				delta, _ := evt["delta"].(string)
				t.lastTurnText.WriteString(delta)
				if t.md == nil {
					t.md = newMdState()
				}
				t.appendLine(t.md.feed(delta))

			case "tool_start":
				// Close previous block with \n\n — each block owns its own trailing space
				if inThinking || inText {
					t.appendLine("\n\n")
					inThinking = false
					inText = false
				}
				name, _ := evt["tool_name"].(string)
				toolID, _ := evt["tool_id"].(string)
				tClr, tIco := toolStyle(name)
				curToolClr = tClr
				toolColors[toolID] = tClr
				toolIcons[toolID] = tIco
				toolNames[toolID] = name
				argBufs[toolID] = ""
				// Write header + empty arg region + empty result region
				// Result region starts empty (invisible); tool_call fills it with ⧖ Executing...
				argRegion := "arg-" + toolID
				resRegion := toolID
				// Reset color before arg region so args render in clrDim, not tClr
				headerLine := tClr + "[::b]" + tIco + " " + tview.Escape(name) + "[-:-:-]" + tClr + "(" + clrReset +
					`["` + argRegion + `"][""]` + tClr + ")" + clrReset + "\n" +
					`["` + resRegion + `"][""]` + "\n\n"
				t.uiOps <- func() {
					if t.toolSlots == nil {
						t.toolSlots = make(map[string]int)
					}
					t.toolSlots[resRegion] = 1
					t.outputBuf.WriteString(headerLine)
				}

			case "tool_args":
				delta, _ := evt["delta"].(string)
				if delta == "" {
					break
				}
				toolID, _ := evt["tool_id"].(string)
				argBufs[toolID] += delta
				current := argBufs[toolID]
				argRegion := "arg-" + toolID
				// Build old and new region tags — replace current content with accumulated
				regStart := `["` + argRegion + `"]`
				regEnd := `[""]`
				safe := strings.ReplaceAll(current, "[", "[[")
				newReg := regStart + clrDim + safe + "[-:-:-]" + regEnd
				t.uiOps <- func() {
					old := t.outputBuf.String()
					// Find the arg region and replace everything between regStart and regEnd
					si := strings.Index(old, regStart)
					if si < 0 {
						return
					}
					ei := strings.Index(old[si+len(regStart):], regEnd)
					if ei < 0 {
						return
					}
					ei = si + len(regStart) + ei + len(regEnd)
					newContent := old[:si] + newReg + old[ei:]
					t.outputBuf.Reset()
					t.outputBuf.WriteString(newContent)
				}

			case "tool_call":
				toolID, _ := evt["tool_id"].(string)
				toolArgs, _ := evt["tool_args"].(string)
				tc := toolColors[toolID]
				if tc == "" {
					tc = curToolClr
				}
				tName := toolNames[toolID]
				if tName == "" {
					tName, _ = evt["tool_name"].(string)
				}
				// Final args: compact (strip braces, full length)
				tClr2, _ := toolStyle(tName)
				args := strings.TrimSpace(toolArgs)
				args = strings.TrimPrefix(args, "{")
				args = strings.TrimSuffix(args, "}")
				args = strings.TrimSpace(args)
				// no truncation — show full args
				argRegion := "arg-" + toolID
				regStart := `["` + argRegion + `"]`
				regEnd := `[""]`
				finalReg := regStart + clrDim + strings.ReplaceAll(args, "[", "[[") + "[-:-:-]" + regEnd
				// Finalize arg region + fill result region with ⧖ Executing...
				resRegion := `["` + toolID + `"]`
				emptyRes := resRegion + `[""]`
				execRes := resRegion + tClr2 + "\u29d6" + clrReset + clrDim + " Executing..." + clrReset + `[""]`
				t.uiOps <- func() {
					old := t.outputBuf.String()
					// 1. Finalize arg region
					si := strings.Index(old, regStart)
					if si >= 0 {
						ei := strings.Index(old[si+len(regStart):], regEnd)
						if ei >= 0 {
							ei = si + len(regStart) + ei + len(regEnd)
							old = old[:si] + finalReg + old[ei:]
						}
					}
					// 2. Replace empty result region with ⧖ Executing...
					old = strings.Replace(old, emptyRes, execRes, 1)
					t.outputBuf.Reset()
					t.outputBuf.WriteString(old)
				}

			case "tool_result":
				toolID, _ := evt["tool_id"].(string)
				toolNameRes, _ := evt["tool_name"].(string)
				output, _ := evt["output"].(string)
				dur, _ := floatFromMap(evt, "duration")
				isErr, _ := evt["is_error"].(bool)
				var result string
				if isErr {
					clean := stripANSI(strings.TrimSpace(output))
					lines := strings.Split(clean, "\n")
					// First line inline after icon
					first := strings.ReplaceAll(strings.TrimSpace(lines[0]), "[", "[[")
					var sb strings.Builder
					sb.WriteString(fmt.Sprintf(clrErr+"✘"+clrReset+" "+clrDim+"[%s] %s"+clrReset+"\n", formatDur(dur), first))
					// Up to 2 detail lines indented
					detail := lines[1:]
					shown := 0
					for _, l := range detail {
						l = strings.TrimSpace(l)
						if l == "" {
							continue
						}
						if shown >= 2 {
							break
						}
						sb.WriteString("  " + clrDim + strings.ReplaceAll(l, "[", "[[") + clrReset + "\n")
						shown++
					}
					// Count remaining non-empty lines
					extra := 0
					for _, l := range detail[shown:] {
						if strings.TrimSpace(l) != "" {
							extra++
						}
					}
					if extra > 0 {
						sb.WriteString(fmt.Sprintf("  "+clrDim+fmt.Sprintf("... (+%d lines)", extra)+clrReset+"\n", extra))
					}
					sb.WriteString("\n")
					result = sb.String()
				} else {
					lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
					count := len(lines)
					if count == 1 && lines[0] == "" {
						count = 0
					}
					result = fmt.Sprintf(clrOK+"✔"+clrReset+" "+clrDim+"[%s] (%d lines)"+clrReset+"\n\n", formatDur(dur), count)
				}
				t.fillSlot(toolID, toolNameRes, result)

			case "compact_start":
				t.compactStart = time.Now()
				t.appendLine("\n" + clrAccent + "[::b]◎ Compacting[-:-:-]" + clrReset + "\n")

			case "compact_end":
				dur := time.Since(t.compactStart).Milliseconds()
				t.appendLine(fmt.Sprintf(clrOK+"✔"+clrReset+" "+clrDim+"[%s]"+clrReset+"\n\n", formatDur(float64(dur))))
				t.spinning = false
				t.app.QueueUpdateDraw(func() { t.updateInfo() })

			case "stop":
				t.appendLine("\n\n" + clrWarn + "⏹ Stopped" + clrReset + "\n\n")
				t.spinning = false

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
				if t.md != nil {
					if tail := t.md.flush(); tail != "" {
						t.appendLine(tail)
					}
					t.md = nil
				}
				// Always reset tview style at turn end to prevent bleed into next block
				t.appendLine(clrReset)
				if inThinking || inText {
					t.appendLine("\n\n")
				}
				inThinking = false
				inText = false
				// Clear slot tracking for next turn
				t.app.QueueUpdateDraw(func() { t.toolSlots = nil })
				if t.queueCount > 0 && len(t.localQueue) > 0 {
					msg := t.localQueue[0]
					t.localQueue = t.localQueue[1:]
					t.queueCount--
					t.appendLine(clrPrimary + "❯ " + msg + clrReset + "\n\n")
					t.spinning = true // next turn starting — Esc can stop it
					t.app.QueueUpdateDraw(func() { t.updateInfo() })
				} else {
					t.spinning = false
				}

			case "error":
				msg, _ := evt["message"].(string)
				t.appendLine(fmt.Sprintf(clrErr+"✘ %s"+clrReset+"\n\n", msg))
				t.spinning = false
				// Persistent SSE — keep streaming
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
	var spinStart time.Time
	for {
		select {
		case <-t.spinnerStop:
			return
		case <-ticker.C:
			if !t.spinning {
				spinStart = time.Time{}
				t.app.QueueUpdateDraw(func() {
					t.spinner.SetText("\n\n")
				})
				continue
			}
			if spinStart.IsZero() {
				spinStart = time.Now()
			}
			f := spinnerFrames[frame%len(spinnerFrames)]
			// Label changes every ~5s (5000ms / 80ms = ~62 frames)
			lbl := spinnerLabels[(frame/62)%len(spinnerLabels)]
			elapsed := time.Since(spinStart).Seconds()
			var timeStr string
			if elapsed < 60 {
				timeStr = fmt.Sprintf("%.0fs", elapsed)
			} else {
				timeStr = fmt.Sprintf("%dm%ds", int(elapsed)/60, int(elapsed)%60)
			}
			frame++
			t.app.QueueUpdateDraw(func() {
				t.spinner.SetText(fmt.Sprintf("\n"+clrAccent+"%s"+clrReset+" "+clrDim+"%s... [%s]"+clrReset+"\n", f, lbl, timeStr))
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
	t.info.SetText(fmt.Sprintf(clrDim+"%s • %s%s"+clrReset, loc, name, queue))

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
		clrDim+"↑%s ↓%s%s %s %.1f%%/%s %s%s"+clrReset,
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

// toolStyle returns the color tag and icon for a given tool name.
func toolStyle(name string) (clr, icon string) {
	switch name {
	case "Skill":
		return clrAccent, "✦"
	case "Subagent":
		return clrAccent, "⬢"
	default:
		return clrAccent, "⚙"
	}
}

func summarize(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

// formatToolOutput formats tool output for display: first 3 lines + (+N lines) count.
// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // skip 'm'
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func formatDur(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.0fms", ms)
}

func formatToolOutput(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if len(lines) == 1 {
		return "  " + clrDim + strings.ReplaceAll(lines[0], "[", "[[") + clrReset + "\n"
	}
	return fmt.Sprintf("  "+clrDim+fmt.Sprintf("(%d lines)", len(lines))+clrReset+"\n", len(lines))
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
