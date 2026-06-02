package tuiv2

import (
	"context"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/agent"
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

	streaming        bool
	agentLineStarted bool
	cancelFn         context.CancelFunc

	// Command palette
	cmds     []paletteCmd // available commands
	cmdSel   int      // selected index
	cmdOpen  bool     // palette visible
}

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
	t.output.SetWrap(term.Width()-3, "   ") // wrap with 3-space continuation indent
	t.input = NewInput("Type a message...", term.Width(), t.submit)
	if model != "" {
		if sess, err := a.NewSession(".", model); err == nil {
			t.session = sess
			t.session.Subscribe(func(e types.Event) { t.events <- e })
		}
	}
	return t
}

func (t *TUI) Run(ctx context.Context) error {
	defer t.term.Restore()
	t.term.HideCursor()
	defer t.term.ShowCursor()
	t.term.Clear() // clear on startup — removes shell artifacts

	t.printBanner()
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

	// Command palette below input — PI-style, max 5 visible
	if t.cmdOpen && len(t.cmds) > 0 {
		maxVisible := 5
		total := len(t.cmds)
		start := 0
		end := total
		if total > maxVisible {
			// Scroll window around selection
			start = t.cmdSel - maxVisible/2
			if start < 0 { start = 0 }
			end = start + maxVisible
			if end > total { end = total; start = end - maxVisible }
		}
		for i := start; i < end; i++ {
			cmd := t.cmds[i]
			padded := cmd.name + strings.Repeat(" ", 16-len(cmd.name))
			if i == t.cmdSel {
				lines = append(lines, " \033[32m→ "+cmd.name+strings.Repeat(" ", 16-len(cmd.name))+"\033[0m\033[90m"+cmd.desc+"\033[0m")
			} else {
				lines = append(lines, "   "+padded+"\033[90m"+cmd.desc+"\033[0m")
			}
		}
		if total > maxVisible {
			lines = append(lines, fmt.Sprintf("   \033[90m(%d/%d)\033[0m", t.cmdSel+1, total))
		}
	}
	if f := t.footer.Render(width); len(f) > 0 {
		lines = append(lines, f...)
	}
	t.term.Clear()
	t.term.WriteString("\033[?2026h")
	for i, line := range lines {
		if i > 0 { t.term.WriteString("\r\n") }
		t.term.WriteString(line)
	}
	t.term.WriteString("\033[?2026l")
}

type paletteCmd struct { name, desc string }

var paletteCmds = []paletteCmd{
	{"model", "Select model"},
	{"thinking", "Set thinking level"},
	{"connect", "Connect provider"},
	{"disconnect", "Disconnect provider"},
	{"clear", "Clear conversation"},
	{"help", "Show commands"},
	{"exit", "Exit harness"},
}

func (t *TUI) handleKey(data []byte) bool {
	// Up/Down arrows when palette is open
	if t.cmdOpen && len(data) >= 3 && data[0] == 27 && data[1] == '[' {
		switch data[2] {
		case 'A': // Up
			t.cmdSel--
			if t.cmdSel < 0 { t.cmdSel = len(t.cmds) - 1 }
			return true
		case 'B': // Down
			t.cmdSel++
			if t.cmdSel >= len(t.cmds) { t.cmdSel = 0 }
			return true
		}
	}

	// Tab when palette open — autocomplete into input
	if t.cmdOpen && data[0] == '\t' && len(t.cmds) > 0 {
		t.input.value = "/" + t.cmds[t.cmdSel].name + " "
		t.cmdOpen = false
		return true
	}

	// Enter when palette open — execute command directly
	if t.cmdOpen && (data[0] == '\r' || data[0] == '\n') && len(t.cmds) > 0 {
		cmd := t.cmds[t.cmdSel].name
		t.input.value = ""
		t.cmdOpen = false
		if t.input.onSubmit != nil {
			t.input.onSubmit("/" + cmd)
		}
		return true
	}

	// Esc when palette open — dismiss
	if t.cmdOpen && data[0] == 27 {
		t.input.value = ""
		t.cmdOpen = false
		return true
	}

	// Pass to input handler
	changed := t.input.HandleKey(data)

	// Check for / prefix to open palette
	if strings.HasPrefix(t.input.value, "/") {
		if !t.cmdOpen {
			t.cmdOpen = true
			t.cmdSel = 0
		}
		t.refreshCmds()
	} else if t.cmdOpen {
		t.cmdOpen = false
		t.cmds = nil
	}

	return changed
}

func (t *TUI) refreshCmds() {
	filter := strings.TrimPrefix(t.input.value, "/")
	if filter == "" {
		t.cmds = paletteCmds
		return
	}
	t.cmds = nil
	for _, c := range paletteCmds {
		if strings.HasPrefix(c.name, filter) { t.cmds = append(t.cmds, c) }
	}
	if t.cmdSel >= len(t.cmds) { t.cmdSel = 0 }
}

func (t *TUI) submit(text string) {
	if text == "" { return }
	if strings.HasPrefix(text, "/") {
		t.output.Add("\033[90m  /" + strings.TrimPrefix(text, "/") + "\033[0m")
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
					t.output.Add(" \033[96m← \033[0m" + part)
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
		if e.IsError { mark = "✗" }
		t.output.Add(fmt.Sprintf("  %s %s [%s]", mark, trunc(e.Output, 60), e.Duration.Round(1000000)))
	case types.EventTokens:
		t.footer.Set(BuildFooter(
			e.Tokens.Input, int(e.Tokens.TotalOutput),
			e.Tokens.CacheRead, e.Tokens.CacheWrite,
			e.Tokens.CostUSD, e.Tokens.ContextUsage, e.Tokens.ContextWindow,
			t.model,
		))
	case types.EventTurnEnd:
		t.streaming = false
		t.agentLineStarted = false
	case types.EventError:
		t.output.Add("  \033[31m✗ " + e.Output + "\033[0m")
		t.streaming = false
	}
}

func (t *TUI) printBanner() {
	t.output.Add("")
	t.output.Add("  \033[96m╦ ╦╔═╗╦═╗╔╗╔╔═╗╔═╗╔═╗\033[0m")
	t.output.Add("  \033[96m╠═╣╠═╣╠╦╝║║║║╣ ╚═╗╚═╗\033[0m")
	t.output.Add("  \033[96m╩ ╩╩ ╩╩╚═╝╚╝╚═╝╚═╝╚═╝\033[0m  \033[2mv0.6.0\033[0m")
	t.output.Add("")
	if t.model != "" {
		t.output.Add("  \033[2mmodel: \033[0m\033[96m" + t.model + "\033[0m")
	}
	t.output.Add("")
}

func iconFor(name string) string {
	switch strings.ToLower(name) {
	case "bash": return "⚡"
	case "read": return "📄"
	case "write": return "✏️"
	case "edit": return "🔧"
	case "fetch", "webfetch": return "🔍"
	default: return "🔧"
	}
}

func trunc(s string, max int) string {
	if len(s) <= max { return s }
	return s[:max] + "…"
}
