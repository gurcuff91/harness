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
			if t.input.HandleKey(data) {
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
