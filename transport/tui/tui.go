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

// ── Palette types ─────────────────────────────────────────────────────────

type paletteItem struct{ name, desc string }

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
		if strings.Contains(strings.ToLower(it.name), f) {
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
	thinking    string
	sessionCmds []CommandDef

	// state
	spinning    bool
	spinnerStop chan struct{}
	stats       tokensInfo
}

type tokensInfo struct {
	input, output int
	cost          float64
	contextPct    float64
	contextWin    int
}

func New(a *agent.Agent) *TUI {
	return &TUI{agent: a}
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

	// autoConnect after app is running
	go func() {
		time.Sleep(50 * time.Millisecond)
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

	// Spinner line: 1 line above the separator
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
		AddItem(t.spinner, 1, 0, false).
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
			style := tcell.StyleDefault.Foreground(tcell.ColorGray).Background(tcell.ColorDefault)
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

// rootPaletteItems builds level-1 items from cached session commands + quit.
func (t *TUI) rootPaletteItems() []paletteItem {
	var items []paletteItem
	for _, cmd := range t.sessionCmds {
		items = append(items, paletteItem{cmd.Name, cmd.Description})
	}
	items = append(items, paletteItem{"quit", "Exit harness"})
	return items
}

// cmdType returns: "quit" | "none" | "list" | "free" | "optional"
func (t *TUI) cmdType(cmdName string) string {
	if cmdName == "quit" {
		return "quit"
	}
	for _, cmd := range t.sessionCmds {
		if cmd.Name != cmdName {
			continue
		}
		if len(cmd.Params) == 0 {
			return "none" // compact
		}
		p := cmd.Params[0]
		if !p.Required {
			return "optional" // skill:*
		}
		if len(p.Values) > 0 {
			return "list" // thinking, model
		}
		return "free" // rename
	}
	return "none"
}

func (t *TUI) hasSubPalette(cmdName string) bool {
	return t.cmdType(cmdName) == "list"
}

// getSubItems returns level-2 palette items for a command.
func (t *TUI) getSubItems(cmdName string) []paletteItem {
	for _, cmd := range t.sessionCmds {
		if cmd.Name != cmdName {
			continue
		}
		for _, p := range cmd.Params {
			if len(p.Values) > 0 {
				var items []paletteItem
				for _, v := range p.Values {
					items = append(items, paletteItem{v, ""})
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
		lines = append(lines, "[gray]No matches[-]")
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
				lines = append(lines, fmt.Sprintf("[#5fd7ff]→[-] [white]%s[-]%s[gray]%s[-]", it.name, pad, desc))
			} else {
				lines = append(lines, fmt.Sprintf("[gray]  %s%s%s[-]", it.name, pad, desc))
			}
		}
		// Always show counter when total > maxVisible
		if total > maxVisible {
			lines = append(lines, fmt.Sprintf("[gray](%d/%d)[-]", lv.sel+1, total))
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

	// Resolve model: settings > first available
	if settingsModel != "" && available[settingsModel] {
		t.model = settingsModel
	} else {
		if settingsModel != "" {
			t.showWarn(fmt.Sprintf("Model '%s' is not available. Falling back to first active model.", settingsModel))
		}
		t.model, _ = models[0]["model"].(string)
	}
	t.thinking = settingsThinking

	// Create session
	data, err = t.client.CreateSession(t.model)
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to create session: %s", err.Error()))
		return
	}
	var sess map[string]any
	json.Unmarshal(data, &sess)
	t.sessionID, _ = sess["id"].(string)
	t.sessionName, _ = sess["name"].(string)
	if th, _ := sess["thinking"].(string); th != "" {
		t.thinking = th
	}
	t.loadSessionCommands()
	t.app.QueueUpdateDraw(func() { t.updateInfo() })
}

func (t *TUI) showWarn(msg string) {
	t.app.QueueUpdateDraw(func() {
		fmt.Fprintf(t.output, "[yellow]⚠ %s[-]\n\n", msg)
	})
}

// ── Custom input & palette ──────────────────────────────────────────────────

func (t *TUI) renderInput() {
	if t.inputBuf == "" {
		t.inputTV.SetText("[gray]Type a message or / for commands...[-]")
	} else {
		t.inputTV.SetText(tview.Escape(t.inputBuf) + "[#5fd7ff]█[-]")
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
			case "list":
				// Put /cmd in input with trailing space, open level 2
				subs := t.getSubItems(sel.name)
				t.pal.pushSub(sel.name, subs)
				t.inputBuf = ""
			case "free":
				// Put /cmd (with space) in input, user types value
				t.inputBuf = "/" + sel.name + " "
				t.pal.close()
			case "none", "optional", "quit":
				// Put /cmd in input, close
				t.inputBuf = "/" + sel.name
				t.pal.close()
			}
		} else {
			// Level 2: put /cmd value in input, close
			parentCmd := t.pal.current().parentCmd
			t.inputBuf = "/" + parentCmd + " " + sel.name
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
			case "list":
				// Must choose from sub-palette first
				subs := t.getSubItems(sel.name)
				t.pal.pushSub(sel.name, subs)
				t.inputBuf = ""
				t.redraw()
			case "free":
				// Put /cmd in input with space, user types value
				t.inputBuf = "/" + sel.name + " "
				t.pal.close()
				t.redraw()
			case "none":
				// Execute directly
				cmd := "/" + sel.name
				t.inputBuf = ""
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			case "optional":
				// Execute directly (no param required)
				cmd := "/" + sel.name
				t.inputBuf = ""
				t.pal.close()
				t.redraw()
				go t.handleInput(cmd)
			case "quit":
				t.app.Stop()
			}
		} else {
			// Level 2: execute /cmd value
			parentCmd := t.pal.current().parentCmd
			cmd := "/" + parentCmd + " " + sel.name
			t.inputBuf = ""
			t.pal.close()
			t.redraw()
			go t.handleInput(cmd)
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
			case "list":
				if len(parts) < 2 {
					// Force sub-palette
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
		t.appendLine("[red]No active session.[white]\n")
		return
	}

	t.appendLine(fmt.Sprintf("[#5fd7ff]❯ %s[-]\n\n", text))

	t.spinning = true
	t.client.SendPrompt(t.sessionID, text) //nolint

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
		t.app.Stop()
		return
	}

	if t.sessionID == "" {
		t.appendLine("[red]no active session[-]\n\n")
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
		t.appendLine(fmt.Sprintf("[red]unknown command: %s[-]\n\n", cmd))
		return
	}

	if len(def.Params) > 0 && len(parts) > 1 {
		paramName := def.Params[0].Name
		params[paramName] = strings.Join(parts[1:], " ")
	}

	// Execute via API
	_, err := t.client.ExecCommand(t.sessionID, cmd, params)
	if err != nil {
		t.appendLine(fmt.Sprintf("[red]%s[-]\n\n", err.Error()))
		return
	}

	// Local side-effects (update state before rendering)
	switch cmd {
	case "model":
		if len(parts) > 1 {
			t.model = parts[1]
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
	confirm := fmt.Sprintf("[gray]%s[-]\n\n", strings.Join(parts, " "))
	t.app.QueueUpdateDraw(func() {
		fmt.Fprint(t.output, confirm)
		t.output.ScrollToEnd()
		t.updateInfo()
	})

	switch cmd {
	case "compact":
		// compact triggers SSE events — start streaming
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
	t.appendLine("[gray]models:[white]\n")
	for _, m := range models {
		model, _ := m["model"].(string)
		t.appendLine(fmt.Sprintf("[gray]  %s[white]\n", model))
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
		t.appendLine(fmt.Sprintf("[red]    ✗ %s[white]\n\n", err.Error()))
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
				t.appendLine("[gray]" + strings.ReplaceAll(delta, "[", "[[") + "[-]")

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
				t.appendLine(fmt.Sprintf("[yellow]⚙ %s[-]", name))

			case "tool_args":
				delta, _ := evt["delta"].(string)
				t.appendLine("[gray]" + strings.ReplaceAll(delta, "[", "[[") + "[-]")

			case "tool_call":
				// args done — newline before result line
				t.appendLine("\n")

			case "tool_result":
				output, _ := evt["output"].(string)
				dur, _ := floatFromMap(evt, "duration")
				isErr, _ := evt["is_error"].(bool)
				safe := strings.ReplaceAll(summarize(output), "[", "[[")
				// result + trailing blank line (next block starts clean)
				if isErr {
					t.appendLine(fmt.Sprintf("[red]✗[-] [gray]%s (%.0fms)[-]\n\n", safe, dur))
				} else {
					t.appendLine(fmt.Sprintf("[green]✓[-] [gray]%s (%.0fms)[-]\n\n", safe, dur))
				}

			case "tokens":
				t.stats.input, _ = intFromMap(evt, "input")
				t.stats.output, _ = intFromMap(evt, "total_output")
				t.stats.cost, _ = floatFromMap(evt, "cost_usd")
				t.stats.contextPct, _ = floatFromMap(evt, "context_usage")
				t.stats.contextWin, _ = intFromMap(evt, "context_window")
				t.app.QueueUpdateDraw(func() { t.updateInfo() })

			case "turn_end":
				// close last open block
				if inThinking || inText {
					t.appendLine("\n\n")
				}
				t.spinning = false
				return

			case "error":
				msg, _ := evt["message"].(string)
				t.appendLine(fmt.Sprintf("[red]✗ %s[-]\n\n", msg))
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
					t.flex.ResizeItem(t.spinner, 1, 0)
					t.spinner.SetText("")
				})
				continue
			}
			f := spinnerFrames[frame%len(spinnerFrames)]
			lbl := spinnerLabels[(frame/10)%len(spinnerLabels)]
			frame++
			t.app.QueueUpdateDraw(func() {
				t.flex.ResizeItem(t.spinner, 3, 0)
				t.spinner.SetText(fmt.Sprintf("\n[gray]%s %s...[-]\n", f, lbl))
			})
		}
	}
}

// ── Info / Footer ─────────────────────────────────────────────────────────

func (t *TUI) updateInfo() {
	// info line: cwd (branch) • session name
	cwd, _ := os.Getwd()
	branch := gitBranch(cwd)
	loc := cwd
	if branch != "" {
		loc += " (" + branch + ")"
	}
	name := t.sessionName
	if name == "" {
		name = "No session"
	}
	t.info.SetText(fmt.Sprintf("[gray]%s • %s[-]", loc, name))

	// footer: tokens + model
	if t.model == "" {
		t.footer.SetText("")
		return
	}
	thinking := ""
	if t.thinking != "" && t.thinking != "off" {
		thinking = " • " + t.thinking
	}
	t.footer.SetText(fmt.Sprintf(
		"[gray]↑%s ↓%s $%.4f %.1f%%/%s  %s%s[-]",
		compactNum(t.stats.input),
		compactNum(t.stats.output),
		t.stats.cost,
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
