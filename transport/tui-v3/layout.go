package tuiv3

import (
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
	"github.com/gurcuff91/harness/transport/tui-v3/components"
	"github.com/gurcuff91/harness/transport/tui-v3/render"
	"github.com/gurcuff91/harness/transport/tui-v3/term"
)

// buildUI constructs the render tree and wires input handling.
//
// Layout (top to bottom):
//
//	history    — accumulated conversation output (markdown, tools, etc.)
//	spinner    — animated status line (only while working)
//	─────────  — separator
//	editor     — multi-line input
//	info       — cwd (branch) • session • queue
//	footer     — tokens • cost • context • model • thinking
func (t *TUI) buildUI() {
	t.tui = render.New(term.NewProcessTerminal())

	t.history = components.NewHistory()
	t.spinner = components.NewSpinner(t.tui, "")
	t.editor = components.NewEditor(t.tui, defaultPlaceholder)
	t.info = components.NewTruncatedText("", 0)
	t.footer = components.NewTruncatedText("", 0)
	t.palette = newPaletteController(t)

	// Editor submit → handle input (prompt or command).
	t.editor.OnSubmit = func(text string) {
		t.handleSubmit(text)
	}
	t.editor.OnChange = func(text string) {
		t.palette.onEditorChange(text)
	}
	t.editor.OnEscape = func() {
		t.onEscape()
	}

	// Compose the render tree, mirroring v1's layout exactly:
	//
	//   history   (scrollback)
	//   spinner   (status, only while working)
	//   ───────   sep1  (emerald)
	//   editor    (input)
	//   ───────   sep2  (emerald)
	//   palette   (command list, only when open)
	//   info      (cwd • session • queue)
	//   footer    (tokens • cost • model)
	t.tui.AddChild(t.history)
	t.tui.AddChild(t.spinner)
	t.tui.AddChild(newSeparator()) // sep1
	t.tui.AddChild(t.editor)
	t.tui.AddChild(newSeparator()) // sep2
	t.tui.AddChild(t.palette)      // renders nothing unless open
	t.tui.AddChild(t.info)
	t.tui.AddChild(t.footer)

	// Global input: palette intercepts when open, then editor.
	t.tui.AddInputListener(t.globalInput)
	t.tui.SetFocus(t.editor)
}

// separator is a thin horizontal rule above the editor.
type separator struct{}

func newSeparator() *separator { return &separator{} }

func (s *separator) Render(width int) []string {
	return []string{ansi.Primary(strings.Repeat("─", width))}
}

// globalInput routes input: Ctrl+C/Ctrl+D quit, palette consumes when open.
func (t *TUI) globalInput(data string) bool {
	// Ctrl+C / Ctrl+D at empty editor → quit.
	if data == "\x03" || (data == "\x04" && t.editor.Value() == "") {
		t.quit() // stops SSE + closes the session (flush to disk) + exits
		return true
	}
	// Palette gets first crack at input when open.
	if t.palette.IsOpen() {
		return t.palette.HandleInputConsumed(data)
	}
	return false
}

// onEscape stops an in-flight turn and clears the editor.
func (t *TUI) onEscape() {
	// Cancel an in-progress value capture (e.g. API key entry) first.
	if t.pending != nil {
		cmd := t.pending.cmd
		t.pending = nil
		t.editor.Clear()
		t.editor.SetPlaceholder(defaultPlaceholder)
		t.showWarn("Cancelled: " + cmd)
		t.tui.RequestRender(false)
		return
	}
	if t.spinning && t.sessionID != "" {
		go t.client.StopSession(t.sessionID) //nolint:errcheck
	}
	t.editor.Clear()
	t.tui.RequestRender(false)
}
