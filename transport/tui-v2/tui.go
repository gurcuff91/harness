package tuiv2

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/providers"
	"github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

type TUI struct {
	term   *Terminal
	agent  *agent.Agent
	model  string

	output *Output
	input  *Input
	footer *Footer

	session *agent.Session
	events  chan types.Event

	sessionCwd    string
	sessionName   string
	thinkingLevel string
	streaming   bool
	agentLineStarted bool
	cancelFn         context.CancelFunc
	quitFn           context.CancelFunc // cancels Run's context

	// Command palette
	palette   palette
	paramHint string // shown when param-required cmd is missing its param

	// Pending input: command waiting for user input (e.g. API key)
	pendingCmd    string // e.g. "connect"
	pendingArg    string // e.g. "anthropic"
	pendingPrompt string // e.g. "Insert your API key"
	pendingMasked bool   // mask input (for secrets)
}

// --- Palette ---

type paletteItem struct{ name, desc string }

type paletteLevel struct {
	items     []paletteItem // full list (before filter)
	filter    string        // current filter text
	sel       int           // selected index in filtered
	parentCmd string        // which command opened this sub-level
}

type palette struct {
	open   bool
	levels []paletteLevel // stack: level 0 = commands, level 1 = sub-items
}

var rootCmds = []paletteItem{
	{"model", "Select model"},
	{"thinking", "Set thinking level"},
	{"connect", "Connect provider"},
	{"disconnect", "Disconnect provider"},
	{"compact", "Compact session context"},
	{"rename", "Rename current session"},
	{"resume", "Resume a previous session"},
	{"clear", "Clear conversation"},
	{"exit", "Exit harness"},
}

func (p *palette) depth() int { return len(p.levels) }

func (p *palette) current() *paletteLevel {
	if len(p.levels) == 0 {
		return nil
	}
	return &p.levels[len(p.levels)-1]
}

func (p *palette) filtered() []paletteItem {
	lv := p.current()
	if lv == nil {
		return nil
	}
	if lv.filter == "" {
		return lv.items
	}
	var out []paletteItem
	f := strings.ToLower(lv.filter)
	for _, it := range lv.items {
		if strings.Contains(strings.ToLower(it.name), f) {
			out = append(out, it)
		}
	}
	return out
}

func (p *palette) openRoot() {
	p.open = true
	p.levels = []paletteLevel{{items: rootCmds}}
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

func (t *TUI) closePalette() {
	t.palette.close()
	t.paramHint = ""
}

func (p *palette) setFilter(f string) {
	if lv := p.current(); lv != nil {
		lv.filter = f
		// clamp selection
		filtered := p.filtered()
		if lv.sel >= len(filtered) {
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
	if lv == nil || len(items) == 0 {
		return paletteItem{}, false
	}
	return items[lv.sel], true
}

// --- Palette rendering ---

func (t *TUI) renderPalette() []string {
	if !t.palette.open {
		return nil
	}
	if t.paramHint != "" {
		return []string{"   \033[33m<" + t.paramHint + "> required\033[0m"}
	}
	items := t.palette.filtered()
	if len(items) == 0 {
		return []string{"   \033[2mNo matches found\033[0m"}
	}

	lv := t.palette.current()
	maxVisible := 5
	total := len(items)
	start := 0
	end := total
	if total > maxVisible {
		start = lv.sel - maxVisible/2
		if start < 0 {
			start = 0
		}
		end = start + maxVisible
		if end > total {
			end = total
			start = end - maxVisible
		}
	}

	// Find max name width for alignment
	maxName := 0
	for i := start; i < end; i++ {
		if len(items[i].name) > maxName {
			maxName = len(items[i].name)
		}
	}
	pad := maxName + 2

	var lines []string
	for i := start; i < end; i++ {
		it := items[i]
		spacing := strings.Repeat(" ", pad-len(it.name))
		if i == lv.sel {
			lines = append(lines, " \033[32m→ "+it.name+spacing+"\033[0m\033[90m"+it.desc+"\033[0m")
		} else {
			lines = append(lines, "   "+it.name+spacing+"\033[90m"+it.desc+"\033[0m")
		}
	}
	if total > maxVisible {
		lines = append(lines, fmt.Sprintf("   \033[90m(%d/%d)\033[0m", lv.sel+1, total))
	}
	return lines
}

// --- Sub-palette: model list ---

func (t *TUI) modelItems() []paletteItem {
	providers.EnsureRegistry()
	var items []paletteItem
	for _, p := range providers.All {
		if !p.IsActive() {
			continue
		}
		if len(p.Models()) == 0 {
			_, _ = p.FetchModels()
		}
		for _, m := range p.Models() {
			full := p.Name() + "/" + m.ID
			desc := ""
			if m.ContextWindow > 0 {
				desc = fmt.Sprintf("%dk ctx", m.ContextWindow/1000)
			}
			items = append(items, paletteItem{name: full, desc: desc})
		}
	}
	return items
}

// Commands that have sub-palettes
var subPaletteCmds = map[string]bool{"model": true, "thinking": true, "connect": true, "disconnect": true, "resume": true}

// Commands that require a free-text param (no sub-palette, just input)
var paramRequiredCmds = map[string]string{"rename": "name"}

// --- Key handling ---

func (t *TUI) handleKey(data []byte) bool {
	// Pending input mode: waiting for user to type a value (e.g. API key)
	if t.pendingCmd != "" {
		return t.handlePendingKey(data)
	}

	// Param hint mode: palette is open just for the hint, pass keys to input
	if t.paramHint != "" {
		if data[0] == 27 && len(data) == 1 {
			// Esc — clear hint and input
			t.input.value = ""
			t.closePalette()
			return true
		}
		// Pass to input
		changed := t.input.HandleKey(data)
		// Check if param is now filled
		parts := strings.Fields(t.input.value)
		if len(parts) >= 2 {
			t.paramHint = "" // clear hint, param present
			t.palette.close()
		}
		return changed
	}

	if t.palette.open {
		return t.handlePaletteKey(data)
	}

	// Enter on sub-palette or param-required command
	if (data[0] == '\r' || data[0] == '\n') && strings.HasPrefix(t.input.value, "/") {
		parts := strings.Fields(t.input.value)
		if len(parts) >= 1 {
			cmd := strings.TrimPrefix(parts[0], "/")
			if subPaletteCmds[cmd] && len(parts) < 2 {
				// Incomplete — open sub-palette
				t.palette.openRoot()
				subItems := t.getSubItems(cmd)
				t.palette.pushSub(cmd, subItems)
				t.input.value = ""
				return true
			}
			if paramName, ok := paramRequiredCmds[cmd]; ok && len(parts) < 2 {
				// Missing required param — show hint
				t.palette.open = true
				t.palette.levels = []paletteLevel{{items: nil, filter: "", parentCmd: cmd}}
				t.paramHint = paramName
				return true
			}
		}
	}

	// Pass to input
	changed := t.input.HandleKey(data)

	// Open palette on / — only for bare slash or single-word command
	if strings.HasPrefix(t.input.value, "/") {
		parts := strings.Fields(t.input.value)
		cmd := strings.TrimPrefix(parts[0], "/")
		// Don't open palette if command already has a param
		if len(parts) < 2 || subPaletteCmds[cmd] {
			if !subPaletteCmds[cmd] || len(parts) < 2 {
				t.palette.openRoot()
				t.palette.setFilter(cmd)
			}
		}
	}

	return changed
}

func (t *TUI) handlePaletteKey(data []byte) bool {
	// Arrow keys
	if len(data) >= 3 && data[0] == 27 && data[1] == '[' {
		switch data[2] {
		case 'A':
			t.palette.moveUp()
			return true
		case 'B':
			t.palette.moveDown()
			return true
		}
	}

	// Tab — autocomplete into input
	if data[0] == '\t' {
		sel, ok := t.palette.selected()
		if !ok {
			return false
		}
		if t.palette.depth() == 1 {
			// Level 1: always put in input (param-required cmds need typing)
			t.input.value = "/" + sel.name + " "
			t.closePalette()
		} else {
			// Level 2: autocomplete full command
			parentCmd := t.palette.current().parentCmd
			t.input.value = "/" + parentCmd + " " + sel.name
			t.closePalette()
		}
		return true
	}

	// Enter — execute or drill into sub-palette
	if data[0] == '\r' || data[0] == '\n' {
		sel, ok := t.palette.selected()
		if !ok {
			return false
		}
		if t.palette.depth() == 1 {
			// Level 1
			if subPaletteCmds[sel.name] {
				// Open sub-palette
				t.input.value = ""
				subItems := t.getSubItems(sel.name)
				t.palette.pushSub(sel.name, subItems)
				return true
			}
			if _, needsParam := paramRequiredCmds[sel.name]; needsParam {
				// Needs free-text param — put in input
				t.input.value = "/" + sel.name + " "
				t.closePalette()
				return true
			}
			// No sub-palette, no param — execute directly
			t.input.value = ""
			t.closePalette()
			if t.input.onSubmit != nil {
				t.input.onSubmit("/" + sel.name)
			}
			return true
		}
		// Level 2 — execute with param
		parentCmd := t.palette.current().parentCmd
		t.input.value = ""
		t.closePalette()
		if t.input.onSubmit != nil {
			t.input.onSubmit("/" + parentCmd + " " + sel.name)
		}
		return true
	}

	// Esc — go back or close
	if data[0] == 27 && len(data) == 1 {
		if t.palette.popLevel() {
			t.input.value = "/"
			t.palette.setFilter("")
			return true
		}
		t.input.value = ""
		t.closePalette()
		return true
	}

	// Typing — update filter
	changed := t.input.HandleKey(data)

	// Check if still valid
	if t.palette.depth() == 1 {
		if !strings.HasPrefix(t.input.value, "/") {
			t.closePalette()
		} else {
			filter := strings.TrimPrefix(t.input.value, "/")
			t.palette.setFilter(filter)
		}
	} else {
		// Level 2: filter is the raw input
		t.palette.setFilter(t.input.value)
	}

	return changed
}

func (t *TUI) getSubItems(cmd string) []paletteItem {
	switch cmd {
	case "model":
		return t.modelItems()
	case "thinking":
		return t.thinkingItems()
	case "connect":
		return t.connectItems()
	case "disconnect":
		return t.disconnectItems()
	case "resume":
		return t.resumeItems()
	default:
		return nil
	}
}

func (t *TUI) thinkingItems() []paletteItem {
	return []paletteItem{
		{"off", "Disable thinking"},
		{"low", "Brief reasoning"},
		{"medium", "Standard reasoning"},
		{"high", "Extended reasoning"},
		{"xhigh", "Maximum reasoning"},
	}
}

// isProviderManageable returns true if the provider can be connected/disconnected
// via the TUI. Only credential-activated providers are manageable.
func isProviderManageable(p providers.Provider) bool {
	src := p.ActivationSource()
	return src == providers.ActivationNone || src == providers.ActivationCredentials
}

func (t *TUI) connectItems() []paletteItem {
	providers.EnsureRegistry()
	var items []paletteItem
	for _, p := range providers.All {
		if !p.IsActive() && isProviderManageable(p) {
			status := "inactive"
			switch p.CredentialType() {
			case types.CredTypeAPIKey:
				status = "needs API key"
			case types.CredTypeOAuth:
				status = "needs OAuth"
			}
			items = append(items, paletteItem{name: p.Name(), desc: status})
		}
	}
	return items
}

func (t *TUI) resumeItems() []paletteItem {
	// TODO: connect with real session store
	return []paletteItem{
		{"session-1", "May 24, 2026"},
		{"session-2", "May 23, 2026"},
		{"session-3", "May 22, 2026"},
	}
}

func (t *TUI) disconnectItems() []paletteItem {
	providers.EnsureRegistry()
	var items []paletteItem
	for _, p := range providers.All {
		if p.IsActive() && isProviderManageable(p) {
			if len(p.Models()) == 0 {
					_, _ = p.FetchModels()
			}
			n := len(p.Models())
			desc := fmt.Sprintf("%d models", n)
			items = append(items, paletteItem{name: p.Name(), desc: desc})
		}
	}
	return items
}

// --- TUI lifecycle ---

func New(a *agent.Agent, model string) *TUI {
	term, err := NewTerminal()
	if err != nil {
		panic(err)
	}
	t := &TUI{
		term:   term,
		agent:  a,
		model:  model,
		output: NewOutput(),
		footer: NewFooter(),
		events: make(chan types.Event, 4),
	}
	t.output.SetWrap(term.Width()-3, "   ")
	if cwd, err := os.Getwd(); err == nil {
		t.sessionCwd = cwd
	}
	t.thinkingLevel = config.GetSettingsManager().ThinkingLevel()
	t.input = NewInput("Type a message...", term.Width(), t.submit)
	t.input.onQuit = func() {
		t.output.Add("   \033[2mGoodbye.\033[0m")
		t.render()
		t.shutdown()
	}
	if model != "" {
		if sess, err := a.NewSession(t.sessionCwd, model); err == nil {
			t.session = sess
			t.session.Subscribe(func(e types.Event) { t.events <- e })
		}
	}
	t.updateFooter()
	return t
}

func (t *TUI) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t.quitFn = cancel
	defer t.term.Restore()
	t.term.HideCursor()
	defer t.term.ShowCursor()
	t.term.Clear()

	t.printBanner()
	t.updateFooter()
	t.render()


	for {
		select {
		case <-ctx.Done():
			return nil
		case data := <-t.term.Input():
			if t.handleKey(data) {
				t.render()
			}
		case <-t.term.Resize():
			t.render()
		case e := <-t.events:
			t.handleAgentEvent(e)
			t.render()
		}
	}
}

func (t *TUI) render() {
	width := t.term.Width()
	t.output.SetWrap(width-3, "   ")
	var lines []string
	lines = append(lines, t.output.Lines()...)
	lines = append(lines, "\033[90m"+strings.Repeat("─", width)+"\033[0m")
	lines = append(lines, t.input.Render(width)...)
	lines = append(lines, "\033[90m"+strings.Repeat("─", width)+"\033[0m")

	// Palette
	if pl := t.renderPalette(); len(pl) > 0 {
		lines = append(lines, pl...)
	}

	// Session info line
	lines = append(lines, t.renderSessionInfo())

	if f := t.footer.Render(width); len(f) > 0 {
		lines = append(lines, f...)
	}
	t.term.Clear()
	t.term.WriteString("\033[?2026h")
	for i, line := range lines {
		if i > 0 {
			t.term.WriteString("\r\n")
		}
		t.term.WriteString(line)
	}
	t.term.WriteString("\033[?2026l")
}

func (t *TUI) submit(text string) {
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") {
		t.execCommand(text)
		return
	}
	if t.session == nil {
		t.output.Add("  \033[33m⚠ No provider connected. /connect <provider>\033[0m")
		return
	}
	t.output.Add(" \033[32m→ \033[0m" + text)
	t.output.Add("")
	t.streaming = true
	t.agentLineStarted = false
	ctx, cancel := context.WithCancel(context.Background())
	t.cancelFn = cancel
	go func() {
		_, err := t.session.Prompt(ctx, text, nil)
		if err != nil && !strings.Contains(err.Error(), "canceled") {
			t.events <- types.Event{Type: types.EventError, Output: err.Error()}
			t.render()
		}
	}()
}

func (t *TUI) handleAgentEvent(e types.Event) {
	switch e.Type {
	case types.EventStreamThinkingDelta:
		if !t.agentLineStarted {
			t.output.Add("   \033[2m" + e.Delta)
			t.agentLineStarted = true
		} else {
			t.output.AddStream("\033[2m" + e.Delta)
		}
	case types.EventStreamThinkingEnd:
		t.output.Add("")
		t.agentLineStarted = false
	case types.EventStreamTextDelta:
		parts := strings.Split(e.Delta, "\n")
		for i, part := range parts {
			if i == 0 {
				if !t.agentLineStarted {
					t.output.Add(" \033[96m←\033[0m " + part)
					t.agentLineStarted = true
				} else {
					t.output.AddStream(part)
				}
			} else if part != "" {
				t.output.Add("   " + part)
			}
		}
	case types.EventStreamTextEnd:
		t.output.Add("")
		t.agentLineStarted = false
	case types.EventToolStart:
		t.output.Add(fmt.Sprintf("  %s %s", iconFor(e.ToolName), e.ToolName))
	case types.EventToolCall:
		t.output.Add(fmt.Sprintf("  %s %s %s", iconFor(e.ToolName), e.ToolName, trunc(e.ToolArgs, 40)))
	case types.EventToolResult:
		mark := "✓"
		if e.IsError {
			mark = "✗"
		}
		t.output.Add(fmt.Sprintf("  %s %s [%s]", mark, trunc(e.Output, 60), e.Duration.Round(1000000)))
	case types.EventTokens:
		t.footer.Set(BuildFooter(
			e.Tokens.Input, int(e.Tokens.TotalOutput),
			e.Tokens.CacheRead, e.Tokens.CacheWrite,
			e.Tokens.CostUSD, e.Tokens.ContextUsage, e.Tokens.ContextWindow,
			t.model, t.thinkingLevel, llm.ModelSupportsThinking(t.model),
		))
	case types.EventTurnEnd:
		t.streaming = false
		t.agentLineStarted = false
	case types.EventError:
		t.output.Add("  \033[31m✗ " + e.Output + "\033[0m")
		t.streaming = false
	}
}

func (t *TUI) updateFooter() {
	contextWindow := 0
	if t.model != "" {
		// Try provider cache first (has real data from API), then fallback to registry
		if meta := t.resolveModelMeta(); meta != nil {
			contextWindow = meta.ContextWindow
		} else if meta := llm.FindMeta(t.model); meta != nil {
			contextWindow = meta.ContextWindow
		}
	}
	t.footer.Set(BuildFooter(
		0, 0, 0, 0, 0, 0, contextWindow,
		t.model, t.thinkingLevel, llm.ModelSupportsThinking(t.model),
	))
}

func (t *TUI) resolveModelMeta() *types.ModelMeta {
	parts := strings.SplitN(t.model, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	providers.EnsureRegistry()
	for _, p := range providers.All {
		if p.Name() == parts[0] {
			return p.ModelMeta(parts[1])
		}
	}
	return nil
}

func (t *TUI) printBanner() {
	t.output.Add("")
	t.output.Add("   \033[96m╦ ╦╔═╗╦═╗╔╗╔╔═╗╔═╗╔═╗\033[0m")
	t.output.Add("   \033[96m╠═╣╠═╣╠╦╝║║║║╣ ╚═╗╚═╗\033[0m")
	t.output.Add("   \033[96m╩ ╩╩ ╩╩╚═╝╚╝╚═╝╚═╝╚═╝\033[0m  \033[2mv0.6.0\033[0m")
	t.output.Add("")
	// Check for active providers
	providers.EnsureRegistry()
	hasActive := false
	for _, p := range providers.All {
		if p.IsActive() {
			hasActive = true
			break
		}
	}
	if !hasActive {
		t.output.Add("   \033[33m⚠ No active providers. Use /connect to add one.\033[0m")
		t.output.Add("")
	}
}

// --- Command execution ---

func (t *TUI) execCommand(text string) {
	parts := strings.Fields(text)
	cmd := strings.TrimPrefix(parts[0], "/")
	arg := ""
	if len(parts) > 1 {
		arg = strings.Join(parts[1:], " ")
	}

	// Show command in output
	t.output.Add("   \033[90m/" + cmd)
	if arg != "" {
		t.output.AddStream(" " + arg)
	}
	t.output.AddStream("\033[0m")

	var msg string
	switch cmd {
	case "exit":
		msg = "Goodbye."
		t.output.Add("   \033[2m" + msg + "\033[0m")
		t.render()
		t.shutdown()
		return
	case "clear":
		t.output.Clear()
		msg = ""
	case "compact":
		if t.session == nil {
			msg = "\033[33mNo active session\033[0m"
		} else {
			msg = "\033[2mCompaction started...\033[0m"
			// TODO: call t.session.Compact()
		}
	case "rename":
		if arg == "" {
			msg = "\033[33m<name> required\033[0m"
		} else if t.session == nil {
			msg = "\033[33mNo active session\033[0m"
		} else {
			msg = "\033[2mSession renamed to: " + arg + "\033[0m"
			// TODO: call t.session.Rename(arg)
		}
	case "resume":
		if arg == "" {
			msg = "\033[33m<session> required\033[0m"
		} else {
			msg = "\033[2mResuming session: " + arg + "\033[0m"
			// TODO: call t.agent.ResumeSession()
		}
	case "model":
		if arg == "" {
			msg = "\033[33m<model> required\033[0m"
		} else {
			if t.session != nil {
				if err := t.session.SwitchModel(context.Background(), arg); err != nil {
					msg = "\033[31m" + err.Error() + "\033[0m"
				} else {
					t.model = arg
					_ = config.GetSettingsManager().SetActiveModel(arg)
					t.updateFooter()
					msg = "\033[32mModel: " + arg + "\033[0m"
				}
			} else {
				msg = "\033[33mNo active session\033[0m"
			}
		}
	case "thinking":
		if arg == "" {
			msg = "\033[33m<level> required\033[0m"
		} else {
			if t.session != nil {
				if err := t.session.SwitchThinking(arg); err != nil {
					msg = "\033[31m" + err.Error() + "\033[0m"
				} else {
					t.thinkingLevel = arg
					_ = config.GetSettingsManager().SetThinkingLevel(arg)
					t.updateFooter()
					msg = "\033[32mThinking: " + arg + "\033[0m"
				}
			} else {
				msg = "\033[33mNo active session\033[0m"
			}
		}
	case "connect":
		if arg == "" {
			msg = "\033[33m<provider> required\033[0m"
		} else {
			// Find provider and check credential type
			providers.EnsureRegistry()
			var target providers.Provider
			for _, p := range providers.All {
				if p.Name() == arg {
					target = p
					break
				}
			}
			if target == nil {
				msg = "\033[31mProvider not found: " + arg + "\033[0m"
			} else if target.CredentialType() == types.CredTypeAPIKey {
				t.startPending("connect", arg, "Enter API key:", true)
				return
			} else if target.CredentialType() == types.CredTypeOAuth {
				t.execConnectOAuth(arg, target)
				return
			} else {
				msg = "\033[33mProvider requires no credentials\033[0m"
			}
		}
	case "disconnect":
		if arg == "" {
			msg = "\033[33m<provider> required\033[0m"
		} else {
			t.execDisconnect(arg)
			return
		}
	default:
		msg = "\033[33mUnknown command: /" + cmd + "\033[0m"
	}

	if msg != "" {
		t.output.Add("   " + msg)
	}
	t.output.Add("")
}

func (t *TUI) renderSessionInfo() string {
	cwd := t.sessionCwd
	if cwd == "" {
		cwd = "~"
	}
	// Shorten home dir
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(cwd, home) {
		cwd = "~" + cwd[len(home):]
	}
	name := ""
	if t.session != nil {
		name = t.session.Name()
	}
	if name == "" {
		name = "no session"
	}
	return " \033[90m" + cwd + " \033[2m•\033[0m \033[90m" + name + "\033[0m"
}

// --- Pending input ---

func (t *TUI) startPending(cmd, arg, prompt string, masked bool) {
	t.pendingCmd = cmd
	t.pendingArg = arg
	t.pendingPrompt = prompt
	t.pendingMasked = masked
	t.input.value = ""
	t.input.placeholder = prompt
	t.output.Add("   \033[2m" + prompt + "\033[0m")
}

func (t *TUI) handlePendingKey(data []byte) bool {
	// Esc — cancel pending
	if data[0] == 27 && len(data) == 1 {
		t.output.Add("   \033[90mcancelled\033[0m")
		t.output.Add("")
		t.clearPending()
		return true
	}

	// Enter — submit pending value
	if data[0] == '\r' || data[0] == '\n' {
		val := strings.TrimSpace(t.input.value)
		if val == "" {
			return true // ignore empty
		}
		// Show masked or truncated value
		if t.pendingMasked {
			t.output.Add("   \033[90m" + maskValue(val) + "\033[0m")
		} else {
			t.output.Add("   \033[90m" + val + "\033[0m")
		}
		// Execute
		t.execPending(val)
		t.clearPending()
		return true
	}

	// Pass to input
	return t.input.HandleKey(data)
}

func (t *TUI) execPending(val string) {
	switch t.pendingCmd {
	case "connect":
		t.execConnect(t.pendingArg, val)
	}
	t.output.Add("")
}

func (t *TUI) clearPending() {
	t.pendingCmd = ""
	t.pendingArg = ""
	t.pendingPrompt = ""
	t.pendingMasked = false
	t.input.value = ""
	t.input.placeholder = "Type a message..."
}

func maskValue(s string) string {
	if len(s) <= 5 {
		return strings.Repeat("*", len(s))
	}
	return s[:5] + strings.Repeat("*", min(len(s)-5, 20))
}

// --- Provider connect ---

func (t *TUI) execConnect(providerName, apiKey string) {
	providers.EnsureRegistry()
	var target providers.Provider
	for _, p := range providers.All {
		if p.Name() == providerName {
			target = p
			break
		}
	}
	if target == nil {
		t.output.Add("   \033[31mProvider not found: " + providerName + "\033[0m")
		return
	}
	// Save credentials and fetch models to validate the key
	creds := types.Credentials{Type: types.CredTypeAPIKey, APIKey: apiKey}
	if err := target.SaveCredentials(creds); err != nil {
		t.output.Add("   \033[31mFailed to save credentials: " + err.Error() + "\033[0m")
		return
	}
	models, err := target.FetchModels()
	if err != nil {
		_ = target.ClearCredentials()
		t.output.Add("   \033[31m" + err.Error() + "\033[0m")
		return
	}
	t.output.Add(fmt.Sprintf("   \033[32mProvider %s connected (%d models)\033[0m", providerName, len(models)))
	t.autoSelectModel(providerName, models)
}

// autoSelectModel picks the first model from the provider and creates a session if needed.
func (t *TUI) autoSelectModel(providerName string, models []types.ModelMeta) {
	if t.session != nil && t.model != "" {
		return // already have a model
	}
	if len(models) == 0 {
		return
	}
	fullModel := providerName + "/" + models[0].ID
	_ = config.GetSettingsManager().SetActiveModel(fullModel)
	t.model = fullModel
	if sess, err := t.agent.NewSession(t.sessionCwd, fullModel); err == nil {
		t.session = sess
		t.session.Subscribe(func(e types.Event) { t.events <- e })
	}
	t.output.Add("   \033[2mModel: " + fullModel + "\033[0m")
	t.updateFooter()
}

func (t *TUI) execDisconnect(providerName string) {
	providers.EnsureRegistry()
	var target providers.Provider
	for _, p := range providers.All {
		if p.Name() == providerName {
			target = p
			break
		}
	}
	if target == nil {
		t.output.Add("   \033[31mProvider not found: " + providerName + "\033[0m")
		t.output.Add("")
		return
	}
	if !target.IsActive() {
		t.output.Add("   \033[33mProvider " + providerName + " is not connected\033[0m")
		t.output.Add("")
		return
	}

	// Clear credentials
	if err := target.ClearCredentials(); err != nil {
		t.output.Add("   \033[31mFailed to disconnect: " + err.Error() + "\033[0m")
		t.output.Add("")
		return
	}

	t.output.Add(fmt.Sprintf("   \033[32mProvider %s disconnected\033[0m", providerName))

	// Check if active model was from this provider
	sm := config.GetSettingsManager()
	activeModel := sm.ActiveModel()
	if strings.HasPrefix(activeModel, providerName+"/") {
		// Find another model from a connected provider
		newModel := ""
		for _, p := range providers.All {
			if p.IsActive() && len(p.Models()) > 0 {
				newModel = p.Name() + "/" + p.Models()[0].ID
				break
			}
		}
		if newModel != "" {
			_ = sm.SetActiveModel(newModel)
			t.model = newModel
			t.output.Add("   \033[2mSwitched to: " + newModel + "\033[0m")
		} else {
			_ = sm.SetActiveModel("")
			t.model = ""
			t.output.Add("   \033[33mNo active providers. Use /connect to add one.\033[0m")
		}
	}
	t.output.Add("")
}

func (t *TUI) execConnectOAuth(providerName string, target providers.Provider) {
	t.output.Add("   \033[2mStarting OAuth authentication...\033[0m")
	t.render()

	// Already connected? (credentials.json has tokens)
	if target.IsActive() {
		models, _ := target.FetchModels()
		t.output.Add(fmt.Sprintf("   \033[32mProvider %s already connected (%d models)\033[0m", providerName, len(models)))
		t.autoSelectModel(providerName, models)
		t.output.Add("")
		return
	}

	// Try importing from keychain (Claude already authed)
	if tokens := providers.ReadClaudeFromKeychain(); tokens != nil {
		if err := target.SaveCredentials(*tokens); err == nil {
			models, _ := target.FetchModels()
			t.output.Add(fmt.Sprintf("   \033[32mProvider %s connected (%d models)\033[0m", providerName, len(models)))
			t.autoSelectModel(providerName, models)
			t.output.Add("")
			return
		}
	}

	// Need to run claude auth login
	t.output.Add("   \033[2mRunning claude auth login...\033[0m")
	t.render()

	// Temporarily suspend raw mode for claude auth login
	t.term.SuspendRaw()
	t.term.ShowCursor()

	cmd := exec.Command("claude", "auth", "login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	// Re-enter raw mode
	exec.Command("stty", "sane").Run()
	t.term.ResumeRaw()
	t.term.HideCursor()

	if runErr != nil {
		t.output.Add("   \033[31mOAuth login failed: " + runErr.Error() + "\033[0m")
		t.output.Add("   \033[90mInstall: npm install -g @anthropic-ai/claude-code\033[0m")
		t.output.Add("")
		return
	}

	// Import tokens from keychain and save to credentials
	tokens := providers.ReadClaudeFromKeychain()
	if tokens == nil {
		t.output.Add("   \033[31mLogin completed but couldn't import tokens from keychain\033[0m")
		t.output.Add("")
		return
	}
	if err := target.SaveCredentials(*tokens); err != nil {
		t.output.Add("   \033[31mFailed to save tokens: " + err.Error() + "\033[0m")
		t.output.Add("")
		return
	}
	models, _ := target.FetchModels()
	t.output.Add(fmt.Sprintf("   \033[32mProvider %s connected (%d models)\033[0m", providerName, len(models)))
	t.autoSelectModel(providerName, models)
	t.output.Add("")
}

func (t *TUI) shutdown() {
	if t.session != nil {
		t.session.Close()
	}
	if t.quitFn != nil {
		t.quitFn()
	}
}

func iconFor(name string) string {
	switch strings.ToLower(name) {
	case "bash":
		return "⚡"
	case "read":
		return "📄"
	case "write":
		return "✏️"
	case "edit":
		return "🔧"
	case "fetch", "webfetch":
		return "🔍"
	default:
		return "🔧"
	}
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
