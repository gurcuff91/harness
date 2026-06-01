package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"golang.org/x/term"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/providers"
	"github.com/gurcuff91/harness/types"
)

// ── Model ────────────────────────────────────────────────────────────────

type model struct {
	a            *agent.Agent
	defaultModel string
	termWidth    int

	// Session
	session   *agent.Session
	modelName string

	// UI components
	input     textinput.Model
	viewport  *linesBuf    // scrollable output above the input
	footer    string       // compact footer line

	// State
	streaming        bool
	agentLineStarted bool
	events           chan types.Event
	cancelFn         context.CancelFunc

	// Command palette
	showCmds   bool
	cmdFilter  string
	cmdSel     int
}

// linesBuf is a simple ring buffer for the scrollable output area.
type linesBuf struct {
	lines  []string
	height int
}

func newLinesBuf(height int) *linesBuf {
	return &linesBuf{lines: make([]string, 0, 256), height: height}
}

func (b *linesBuf) add(line string) {
	b.lines = append(b.lines, line)
}

func (b *linesBuf) write(s string) {
	if len(b.lines) == 0 {
		b.lines = append(b.lines, s)
	} else {
		b.lines[len(b.lines)-1] += s
	}
}

func (b *linesBuf) view() string {
	start := 0
	if len(b.lines) > b.height {
		start = len(b.lines) - b.height
	}
	return strings.Join(b.lines[start:], "\n")
}

// ── Constructor ──────────────────────────────────────────────────────────

func New(a *agent.Agent, defaultModel string) *model {
	ta := textinput.New()
	ta.Placeholder = "Type a message..."
	ta.Width = 80
	ta.Focus()

	// Keyboard: Enter submits, no CharLimit

	w, _, _ := term.GetSize(0)
	if w <= 0 { w = 80 }

	return &model{
		a:            a,
		defaultModel: defaultModel,
		termWidth:    w,
		input:        ta,
		viewport:     newLinesBuf(1000),
		events:       make(chan types.Event, 256),
	}
}

// ── tea.Model interface ──────────────────────────────────────────────────

func (m *model) Init() tea.Cmd {
	m.printBanner()
	return tea.Batch(
		textinput.Blink,
		m.createSession(),
	)
}

type sessionReadyMsg struct {
	session *agent.Session
	err     error
}

func (m *model) createSession() tea.Cmd {
	return func() tea.Msg {
		if m.defaultModel == "" {
			return sessionReadyMsg{err: fmt.Errorf("no model configured")}
		}
		sess, err := m.a.NewSession(".", m.defaultModel)
		return sessionReadyMsg{session: sess, err: err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.input.Width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelCurrentStream()
			return m, tea.Quit

		case "up", "down":
			if m.showCmds {
				if msg.String() == "up" { m.cmdSel-- } else { m.cmdSel++ }
				if m.cmdSel < 0 { m.cmdSel = len(m.filteredCmds()) - 1 }
				if m.cmdSel >= len(m.filteredCmds()) { m.cmdSel = 0 }
				return m, nil
			}

		case "tab", "enter":
			if m.showCmds {
				cmds := m.filteredCmds()
				if m.cmdSel >= 0 && m.cmdSel < len(cmds) {
					sel := "/" + cmds[m.cmdSel]
					m.input.SetValue("")
					m.showCmds = false
					m.cmdSel = 0
					// Execute directly if it needs no args (/help, /clear, /exit, /model)
					m.executeCommand(sel)
				}
				return m, nil
			}
			if msg.String() == "enter" && !m.streaming {
				text := strings.TrimSpace(m.input.Value())
				if text != "" {
					m.submit(text)
				}
				m.input.SetValue("")
				return m, nil
			}
		}

	case sessionReadyMsg:
		if msg.err == nil {
			m.session = msg.session
			m.modelName = m.defaultModel
			m.session.Subscribe(func(e types.Event) { m.events <- e })
			m.viewport.add("  " + ansiDim + "model: " + ansiReset + ansiBCyan + m.modelName + ansiReset)
			m.viewport.add("  " + ansiDim + "/help for commands" + ansiReset)
			m.viewport.add("")
		} else {
			m.viewport.add("  " + ansiYellow + "No provider connected — /connect <provider>" + ansiReset)
		}
		return m, m.listenEvents()

	case agentEventMsg:
		m.handleAgentEvent(msg.event)
		return m, m.listenEvents()
	}

	m.input, cmd = m.input.Update(msg)
	m.checkCommandPalette()
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	sep := ansiGray + strings.Repeat("─", m.termWidth) + ansiReset
	inputView := m.input.View()

	// Footer below input
	footerView := ""
	if m.footer != "" {
		footerView = m.footer + "\n"
	}

	// Command palette overlay above input
	cmdView := ""
	if m.showCmds {
		cmds := m.filteredCmds()
		for i, cmd := range cmds {
			prefix := "  "
			if i == m.cmdSel { prefix = ansiBCyan + "→ " + ansiReset }
			cmdView += prefix + ansiDim + "/" + ansiReset + cmd + "\n"
		}
		if cmdView != "" { cmdView += "\n" }
	}

	output := m.viewport.view()
	if output == "" {
		return sep + "\n" + cmdView + inputView + "\n" + sep + "\n" + footerView
	}
	return output + "\n" + sep + "\n" + cmdView + inputView + "\n" + sep + "\n" + footerView
}

// ── Event handling ───────────────────────────────────────────────────────

type agentEventMsg struct{ event types.Event }

func (m *model) listenEvents() tea.Cmd {
	return func() tea.Msg {
		e, ok := <-m.events
		if !ok { return nil }
		return agentEventMsg{event: e}
	}
}

const (
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiBCyan  = "\033[96m"
	ansiGray   = "\033[90m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

var (
)

func (m *model) handleAgentEvent(e types.Event) {
	switch e.Type {
	case types.EventStreamThinkingDelta:
		m.viewport.write(ansiDim + e.Delta)

	case types.EventStreamThinkingEnd:
		m.viewport.write(ansiReset)
		m.viewport.add("")

	case types.EventStreamTextDelta:
		// Split by newline to maintain indent on continuation lines
		lines := strings.Split(e.Delta, "\n")
		for i, line := range lines {
			if i == 0 {
				if !m.agentLineStarted {
					m.viewport.add(ansiBCyan + "← " + ansiReset + line)
					m.agentLineStarted = true
				} else {
					m.viewport.write(line)
				}
			} else {
				// Continuation line — indent 2 spaces (← is 1 col + space = 2)
				if line != "" {
					m.viewport.add("  " + line)
				}
			}
		}

	case types.EventStreamTextEnd:
		m.viewport.write(ansiReset)
		m.viewport.add("")

	case types.EventToolStart:
		m.viewport.add(fmt.Sprintf("  %s %s", toolIcon(e.ToolName), e.ToolName))

	case types.EventToolCall:
		m.viewport.add(fmt.Sprintf("  %s %s %s", toolIcon(e.ToolName), e.ToolName, trunc(e.ToolArgs, 40)))

	case types.EventToolResult:
		mark := "✓"
		if e.IsError { mark = "✗" }
		m.viewport.add(fmt.Sprintf("  %s %s [%s]", mark, trunc(e.Output, 60), e.Duration.Round(1000000)))

	case types.EventTokens:
		m.footer = ansiDim + "  " + buildFooter(e.Tokens, m.modelName) + ansiReset

	case types.EventTurnEnd:
		m.streaming = false
		m.agentLineStarted = false

	case types.EventError:
		m.viewport.add("  ✗ " + e.Output)
		m.streaming = false
	}
}

// ── Submission ───────────────────────────────────────────────────────────

func (m *model) submit(text string) {
	if strings.HasPrefix(text, "/") {
		m.handleCommand(text)
		return
	}
	if m.session == nil {
		m.viewport.add("  ⚠ No provider connected. /connect <provider>")
		return
	}
	m.viewport.add(ansiGreen + "→ " + ansiReset + text)
	m.viewport.add("")
	m.streaming = true
	m.agentLineStarted = false

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFn = cancel
	go func() {
		_, err := m.session.Prompt(ctx, text, nil)
		if err != nil && !strings.Contains(err.Error(), "canceled") {
			m.events <- types.Event{Type: types.EventError, Output: err.Error()}
		}
	}()
}

// ── Commands ─────────────────────────────────────────────────────────────

var cmdNames = []string{"exit", "quit", "clear", "model", "connect", "disconnect", "thinking", "help"}

// filteredCmds returns commands matching the current input filter
func (m *model) filteredCmds() []string {
	v := strings.TrimPrefix(m.input.Value(), "/")
	if v == "" { return cmdNames }
	var out []string
	for _, cmd := range cmdNames {
		if strings.HasPrefix(cmd, v) { out = append(out, cmd) }
	}
	return out
}

// checkCommandPalette shows/hides the command palette based on input
func (m *model) checkCommandPalette() {
	v := m.input.Value()
	if strings.HasPrefix(v, "/") && !m.showCmds {
		m.showCmds = true
		m.cmdSel = 0
	} else if !strings.HasPrefix(v, "/") && m.showCmds {
		m.showCmds = false
	}
}

func (m *model) handleCommand(input string) {
	parts := strings.Fields(strings.ToLower(input))
	if len(parts) == 0 { return }
	switch parts[0] {
	case "/exit", "/quit", "/q":
		m.cancelCurrentStream()
	case "/clear":
		if m.session != nil { m.session.Close() }
		if sess, err := m.a.NewSession(".", m.modelName); err == nil {
			m.session = sess
			m.session.Subscribe(func(e types.Event) { m.events <- e })
			m.viewport.add("  ✓ History cleared")
		}
	case "/model":
		if len(parts) < 2 {
			m.printModels()
		} else {
			m.switchModel(parts[1])
		}
	case "/connect":
		if len(parts) < 2 {
			m.printProviders()
		} else {
			m.viewport.add(fmt.Sprintf("  Connect %s via /connect in CLI", parts[1]))
		}
	case "/disconnect":
		if len(parts) < 2 {
			m.viewport.add("  Usage: /disconnect <provider>")
		} else {
			m.disconnectProvider(parts[1])
		}
	case "/thinking":
		if len(parts) < 2 {
			m.viewport.add(fmt.Sprintf("  Current: %s  Valid: disable/low/medium/high/xhigh", config.GetSettingsManager().ThinkingLevel()))
		} else {
			level := parts[1]
			config.GetSettingsManager().SetThinkingLevel(level)
			if m.session != nil { m.session.SwitchThinking(level) }
			m.viewport.add("  ✓ Thinking: " + level)
		}
	case "/help":
		m.printHelp()
	default:
		m.viewport.add("  Unknown: " + input + " — /help for list")
	}
}

func (m *model) switchModel(selector string) {
	if !strings.Contains(selector, "/") {
		for _, p := range providers.All {
			if !p.IsActive() { continue }
			for _, md := range p.Models() {
				if md.ID == selector {
					selector = p.Name() + "/" + selector
					break
				}
			}
		}
	}
	if m.session != nil {
		if err := m.session.SwitchModel(context.Background(), selector); err != nil {
			m.viewport.add("  ✗ " + err.Error())
			return
		}
		m.modelName = selector
		config.GetSettingsManager().SetActiveModel(selector)
		m.viewport.add("  ✓ Using " + selector)
		return
	}
	if sess, err := m.a.NewSession(".", selector); err == nil {
		if m.session != nil { m.session.Close() }
		m.session = sess
		m.modelName = selector
		m.session.Subscribe(func(e types.Event) { m.events <- e })
		config.GetSettingsManager().SetActiveModel(selector)
		m.viewport.add("  ✓ Using " + selector)
	} else {
		m.viewport.add("  ✗ " + err.Error())
	}
}

func (m *model) cancelCurrentStream() {
	if m.cancelFn != nil {
		m.cancelFn()
		m.cancelFn = nil
	}
	m.streaming = false
}

// ── Print helpers ────────────────────────────────────────────────────────

func (m *model) printBanner() {
	m.viewport.add("")
	m.viewport.add("  " + ansiBCyan + "╦ ╦╔═╗╦═╗╔╗╔╔═╗╔═╗╔═╗" + ansiReset)
	m.viewport.add("  " + ansiBCyan + "╠═╣╠═╣╠╦╝║║║║╣ ╚═╗╚═╗" + ansiReset)
	m.viewport.add("  " + ansiBCyan + "╩ ╩╩ ╩╩╚═╝╚╝╚═╝╚═╝╚═╝" + ansiReset + ansiDim + "  v0.6.0" + ansiReset)
	m.viewport.add("")

	m.viewport.add("  " + ansiDim + "/help for commands" + ansiReset)
	m.viewport.add("")
}

func (m *model) disconnectProvider(name string) {
	for _, p := range providers.All {
		if p.Name() != name { continue }
		if p.CredentialType() == types.CredTypeNone {
			m.viewport.add(fmt.Sprintf("  %s is auto-detected — cannot disconnect", name))
			return
		}
		if err := p.ClearCredentials(); err != nil {
			m.viewport.add("  ✗ " + err.Error())
			return
		}
		m.viewport.add("  ✓ " + name + " disconnected")
		return
	}
	m.viewport.add("  ✗ Unknown provider: " + name)
}

// executeCommand runs a command immediately, clearing the input
func (m *model) executeCommand(cmd string) {
	m.input.SetValue("")
	m.viewport.add(ansiDim + "  /" + ansiReset + strings.TrimPrefix(cmd, "/"))
	m.viewport.add("")
	m.handleCommand(cmd)
}

func (m *model) printHelp() {
	m.viewport.add("  Commands:")
	m.viewport.add("    /model [provider/model]   — show or switch model")
	m.viewport.add("    /connect <provider>       — connect a provider")
	m.viewport.add("    /disconnect <provider>    — disconnect a provider")
	m.viewport.add("    /thinking [level]         — show or set thinking level")
	m.viewport.add("    /clear                    — reset conversation")
	m.viewport.add("    /exit, /quit              — quit")
	m.viewport.add("  Tab autocompletes commands. Esc cancels streaming.")
}

func (m *model) printModels() {
	groups := providers.GetModelGroups(m.modelName)
	for _, g := range groups {
		m.viewport.add("  " + g.Label)
		for _, md := range g.Models {
			marker := "  "
			if md.Active { marker = "● " }
			m.viewport.add(fmt.Sprintf("  %s%s/%s", marker, md.Provider, md.ID))
		}
	}
}

func (m *model) printProviders() {
	for _, p := range providers.All {
		status := "disconnected"
		if p.IsActive() { status = "connected" }
		m.viewport.add(fmt.Sprintf("  %-18s (%s)", p.Name(), status))
	}
}

// ── Footer + Utilities ───────────────────────────────────────────────────

func buildFooter(tokens types.TokenUsage, model string) string {
	var parts []string
	if tokens.Input > 0 {
		parts = append(parts, "↑"+compactNum(tokens.Input))
	}
	if tokens.TotalOutput > 0 {
		parts = append(parts, "↓"+compactNum(tokens.TotalOutput))
	}
	if tokens.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.3f", tokens.CostUSD))
	}
	if tokens.ContextUsage > 0 && tokens.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("%.1f%%/%s", tokens.ContextUsage*100, compactNum(tokens.ContextWindow)))
	}
	if model != "" {
		parts = append(parts, model)
	}
	return strings.Join(parts, " ")
}

func compactNum(n int) string {
	switch {
	case n >= 1_000_000: return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:     return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:             return fmt.Sprintf("%d", n)
	}
}

func toolIcon(name string) string {
	switch strings.ToLower(name) {
	case "bash":   return "⚡"
	case "read":   return "📄"
	case "write":  return "✏️"
	case "edit":   return "🔧"
	case "fetch", "webfetch": return "🔍"
	default:       return "🔧"
	}
}

func trunc(s string, max int) string {
	if len(s) <= max { return s }
	return s[:max] + "…"
}
