package tui

import (
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/components"
	"github.com/gurcuff91/harness/internal/tui/render"
	"github.com/gurcuff91/harness/internal/tui/term"
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
		wasBash := t.bashMode
		t.bashMode = strings.HasPrefix(text, "!")
		if t.bashMode != wasBash {
			t.tui.RequestRender(false)
		}
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
	sep1 := newEditorSeparator(t.editor) // sep1 — shows "↑ N more" on overflow
	sep2 := newSeparator()               // sep2
	// Both separators share the same colorFn so they flip together when the
	// user enters bash mode (input starts with "!").
	sepColor := func() string {
		if t.bashMode {
			return ansi.HexWarn // amber — signals bash execution mode
		}
		return ansi.HexPrimary
	}
	sep1.colorFn = sepColor
	sep2.colorFn = sepColor
	t.editor.ColorFn = sepColor

	t.tui.AddChild(t.history)
	t.tui.AddChild(t.spinner)
	t.tui.AddChild(sep1)
	t.tui.AddChild(t.editor)
	t.tui.AddChild(sep2)
	t.tui.AddChild(t.palette) // renders nothing unless open
	t.tui.AddChild(t.info)
	t.tui.AddChild(t.footer)

	// Global input: palette intercepts when open, then editor.
	t.tui.AddInputListener(t.globalInput)
	t.tui.SetFocus(t.editor)
}

// separator is a thin horizontal rule. When bound to an editor and that editor
// has input scrolled off the top, it renders a left-aligned "↑ N more" hint
// embedded in the rule (mirrors PI's overflow indicator).
//
// colorFn, when set, overrides the default primary (teal) color — used by the
// bash-mode indicator to switch both separators to rose while "!" is typed.
type separator struct {
	editor  *components.Editor // nil = plain rule
	colorFn func() string      // returns a hex color; nil = HexPrimary
}

func newSeparator() *separator { return &separator{} }

// newEditorSeparator binds a separator to the editor so it can show the overflow
// hint above the visible input window.
func newEditorSeparator(e *components.Editor) *separator { return &separator{editor: e} }

func (s *separator) color() string {
	if s.colorFn != nil {
		return s.colorFn()
	}
	return ansi.HexPrimary
}

func (s *separator) Render(width int) []string {
	col := s.color()
	if s.editor != nil {
		if n := s.editor.HiddenAbove(width); n > 0 {
			return []string{labeledRule(width, fmt.Sprintf("↑ %d more", n), col)}
		}
	}
	return []string{ansi.FG(col, strings.Repeat("─", width))}
}

// labeledRule draws a left-aligned label embedded in a colored rule:
//
//	── ↑ 2 more ─────────────────────────────
//
// The lead-in is two dashes, then the muted label, then dashes filling the rest.
func labeledRule(width int, label, hexColor string) string {
	const lead = 2
	lw := ansi.VisibleWidth(label)
	// lead dashes + space + label + space, then fill the remainder.
	used := lead + 1 + lw + 1
	if used >= width {
		return ansi.FG(hexColor, strings.Repeat("─", width))
	}
	trail := width - used
	return ansi.FG(hexColor, strings.Repeat("─", lead)) + " " +
		ansi.Muted(label) + " " +
		ansi.FG(hexColor, strings.Repeat("─", trail))
}

// globalInput routes input: Ctrl+C/Ctrl+D quit, Ctrl+V pastes a clipboard
// image, palette consumes when open.
//
// Note: the TUI does not implement in-app history scrolling. It is an INLINE
// renderer that always sticks to the bottom (tail-follow) while the agent
// streams; the terminal's own native scrollback is used to read earlier
// output once a turn completes. This keeps rendering simple and avoids the
// fragile cursor math that in-app scrolling required on inline TUIs.
func (t *TUI) globalInput(data string) bool {
	// Ctrl+C / Ctrl+D at empty editor → quit.
	if data == "\x03" || (data == "\x04" && t.editor.Value() == "") {
		t.quit() // stops SSE + closes the session (flush to disk) + exits
		return true
	}
	// Ctrl+V (0x16) → paste a clipboard image as a path token. Cmd+V can't be
	// intercepted in a raw-mode terminal (the terminal owns it), so Ctrl+V is the
	// portable trigger. Text pastes still arrive via bracketed paste to the editor.
	if data == "\x16" {
		t.pasteClipboardImage()
		return true
	}
	// Palette gets first crack at input when open.
	if t.palette.IsOpen() {
		return t.palette.HandleInputConsumed(data)
	}

	return false
}

// pasteClipboardImage reads a PNG from the clipboard (off the input thread),
// writes it to a temp file, and inserts the path into the editor as text. The
// Read tool resolves image paths, so the agent receives the image by reading it.
func (t *TUI) pasteClipboardImage() {
	go func() {
		path, err := PasteImageFromClipboard()
		switch {
		case err != nil:
			t.showWarn("clipboard image: " + err.Error())
		case path == "":
			t.showWarn("no image in clipboard")
		default:
			t.editor.InsertText(path)
		}
		t.tui.RequestRender(false)
	}()
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
	if t.isSpinning() && t.sessionID != "" {
		go t.client.StopSession(t.sessionID) //nolint:errcheck
	}
	t.editor.Clear()
	t.tui.RequestRender(false)
}
