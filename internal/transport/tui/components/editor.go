package components

import (
	"strings"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
	"github.com/gurcuff91/harness/internal/transport/tui/keys"
)

// maxEditorRows caps the rendered height of the editor (matches v1's 5-line cap).
const maxEditorRows = 5

// Editor is a multi-line text input with a fake block cursor, placeholder text,
// word-wise navigation, and paste handling. Port of the harness's v1 input
// (transport/tui), built on the Component model.
//
// Newline insertion uses Ctrl+J (LF) or Alt+Enter (both reliable without the
// Kitty protocol — see the TODO in term/terminal.go). Enter (\r) submits.
type Editor struct {
	buf         []rune
	cursor      int // rune index into buf
	placeholder string

	// Callbacks.
	OnSubmit func(text string)
	OnChange func(text string)
	OnEscape func()

	// DisableSubmit, when true, makes Enter insert a newline instead of
	// submitting (used while an overlay/palette owns the Enter key).
	DisableSubmit bool

	renderer Renderer
}

// NewEditor creates an editor bound to a renderer (for repaint on edits).
func NewEditor(r Renderer, placeholder string) *Editor {
	return &Editor{renderer: r, placeholder: placeholder}
}

// Value returns the current text.
func (e *Editor) Value() string { return string(e.buf) }

// SetValue replaces the text and moves the cursor to the end.
func (e *Editor) SetValue(s string) {
	e.buf = []rune(s)
	e.cursor = len(e.buf)
	e.changed()
}

// InsertText inserts s at the cursor position (e.g. a pasted image path). When
// text already precedes the cursor, a separating space is added so the inserted
// token doesn't fuse with the previous word.
func (e *Editor) InsertText(s string) {
	if e.cursor > 0 && e.buf[e.cursor-1] != ' ' && e.buf[e.cursor-1] != '\n' {
		s = " " + s
	}
	e.insert(s)
}

// Clear empties the editor.
func (e *Editor) Clear() {
	e.buf = nil
	e.cursor = 0
	e.changed()
}

// SetPlaceholder changes the dim hint shown when the editor is empty.
func (e *Editor) SetPlaceholder(s string) { e.placeholder = s }

// Placeholder returns the current placeholder text.
func (e *Editor) Placeholder() string { return e.placeholder }

// CursorPos returns the rune index of the cursor.
func (e *Editor) CursorPos() int { return e.cursor }

// HandleInput processes a single input sequence.
func (e *Editor) HandleInput(data string) {
	// Bracketed paste arrives wrapped in markers.
	if strings.HasPrefix(data, ansi.PasteStart) {
		content := strings.TrimPrefix(data, ansi.PasteStart)
		content = strings.TrimSuffix(content, ansi.PasteEnd)
		// Normalize clipboard line endings: CRLF and bare CR both become LF.
		// A raw \r moves the cursor to column 0 without advancing a line, so each
		// pasted line would overwrite the previous one (e.g. "Key west"+"TFCGKE"
		// → "KeytiCGKE"). Also strips \r that would corrupt the sent message.
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		e.insert(content)
		return
	}

	if k, ok := keys.Lookup(data); ok {
		switch k {
		case keys.Enter:
			if e.DisableSubmit {
				e.insert("\n")
				return
			}
			if e.OnSubmit != nil {
				e.OnSubmit(string(e.buf))
			}
			return
		case keys.AltEnter, keys.CtrlJ:
			// Insert a newline. Ctrl+J (LF) is the reliable terminal newline;
			// Alt+Enter is kept as an alternative. Shift+Enter can't be told
			// apart from Enter without the Kitty keyboard protocol.
			e.insert("\n")
			return
		case keys.Escape:
			if e.OnEscape != nil {
				e.OnEscape()
			}
			return
		case keys.Backspace:
			e.backspace()
			return
		case keys.Delete:
			e.deleteForward()
			return
		case keys.Left:
			e.moveLeft()
			return
		case keys.Right:
			e.moveRight()
			return
		case keys.Up:
			e.moveUp()
			return
		case keys.Down:
			e.moveDown()
			return
		case keys.Home, keys.CtrlA:
			e.cursor = e.lineStart(e.cursor)
			e.repaint()
			return
		case keys.End, keys.CtrlE:
			e.cursor = e.lineEnd(e.cursor)
			e.repaint()
			return
		case keys.CtrlU:
			e.deleteToLineStart()
			return
		case keys.CtrlK:
			e.deleteToLineEnd()
			return
		case keys.CtrlW, keys.AltBackspace:
			e.deleteWordBack()
			return
		case keys.CtrlLeft, keys.AltLeft:
			e.cursor = e.wordLeft(e.cursor)
			e.repaint()
			return
		case keys.CtrlRight, keys.AltRight:
			e.cursor = e.wordRight(e.cursor)
			e.repaint()
			return
		case keys.Tab, keys.ShiftTab, keys.CtrlC, keys.CtrlD, keys.CtrlY, keys.CtrlV:
			// Not handled by the editor itself — ignored (owner intercepts).
			return
		}
	}

	if keys.IsPrintable(data) {
		e.insert(data)
	}
}

// ── Editing primitives ──────────────────────────────────────────────────────

func (e *Editor) insert(s string) {
	r := []rune(s)
	next := make([]rune, 0, len(e.buf)+len(r))
	next = append(next, e.buf[:e.cursor]...)
	next = append(next, r...)
	next = append(next, e.buf[e.cursor:]...)
	e.buf = next
	e.cursor += len(r)
	e.changed()
}

func (e *Editor) backspace() {
	if e.cursor > 0 {
		e.buf = append(e.buf[:e.cursor-1], e.buf[e.cursor:]...)
		e.cursor--
		e.changed()
	}
}

func (e *Editor) deleteForward() {
	if e.cursor < len(e.buf) {
		e.buf = append(e.buf[:e.cursor], e.buf[e.cursor+1:]...)
		e.changed()
	}
}

func (e *Editor) deleteToLineStart() {
	start := e.lineStart(e.cursor)
	e.buf = append(e.buf[:start], e.buf[e.cursor:]...)
	e.cursor = start
	e.changed()
}

func (e *Editor) deleteToLineEnd() {
	end := e.lineEnd(e.cursor)
	e.buf = append(e.buf[:e.cursor], e.buf[end:]...)
	e.changed()
}

func (e *Editor) deleteWordBack() {
	target := e.wordLeft(e.cursor)
	if target < e.cursor {
		e.buf = append(e.buf[:target], e.buf[e.cursor:]...)
		e.cursor = target
		e.changed()
	}
}

// ── Cursor movement ─────────────────────────────────────────────────────────

func (e *Editor) moveLeft() {
	if e.cursor > 0 {
		e.cursor--
		e.repaint()
	}
}

func (e *Editor) moveRight() {
	if e.cursor < len(e.buf) {
		e.cursor++
		e.repaint()
	}
}

func (e *Editor) moveUp() {
	start := e.lineStart(e.cursor)
	if start == 0 {
		return // already on first line
	}
	col := e.cursor - start
	prevEnd := start - 1
	prevStart := e.lineStart(prevEnd)
	prevLen := prevEnd - prevStart
	if col > prevLen {
		col = prevLen
	}
	e.cursor = prevStart + col
	e.repaint()
}

func (e *Editor) moveDown() {
	end := e.lineEnd(e.cursor)
	if end >= len(e.buf) {
		return // already on last line
	}
	col := e.cursor - e.lineStart(e.cursor)
	nextStart := end + 1
	nextEnd := e.lineEnd(nextStart)
	nextLen := nextEnd - nextStart
	if col > nextLen {
		col = nextLen
	}
	e.cursor = nextStart + col
	e.repaint()
}

func (e *Editor) lineStart(pos int) int {
	for pos > 0 && e.buf[pos-1] != '\n' {
		pos--
	}
	return pos
}

func (e *Editor) lineEnd(pos int) int {
	for pos < len(e.buf) && e.buf[pos] != '\n' {
		pos++
	}
	return pos
}

func (e *Editor) wordLeft(pos int) int {
	for pos > 0 && e.buf[pos-1] == ' ' {
		pos--
	}
	for pos > 0 && e.buf[pos-1] != ' ' && e.buf[pos-1] != '\n' {
		pos--
	}
	return pos
}

func (e *Editor) wordRight(pos int) int {
	for pos < len(e.buf) && e.buf[pos] == ' ' {
		pos++
	}
	for pos < len(e.buf) && e.buf[pos] != ' ' && e.buf[pos] != '\n' {
		pos++
	}
	return pos
}

// ── Render ──────────────────────────────────────────────────────────────────

// Render produces the editor lines: placeholder when empty, otherwise the text
// with a fake block cursor. Output is capped to maxEditorRows visible lines,
// scrolled to keep the cursor in view.
func (e *Editor) Render(width int) []string {
	if len(e.buf) == 0 {
		return []string{ansi.Dimmed(e.placeholder)}
	}
	visible, _ := e.layout(width)
	return visible
}

// layout computes the editor's wrapped, cursor-windowed visible lines and how
// many lines are scrolled off the top. It is pure (no stored state), so callers
// — including the separator above the editor — always get a value consistent
// with the CURRENT buffer, independent of child render order.
func (e *Editor) layout(width int) (visible []string, hiddenAbove int) {
	if len(e.buf) == 0 {
		return []string{ansi.Dimmed(e.placeholder)}, 0
	}

	cursor := e.cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(e.buf) {
		cursor = len(e.buf)
	}

	before := string(e.buf[:cursor])
	atCursor := " "
	after := ""
	if cursor < len(e.buf) {
		if e.buf[cursor] == '\n' {
			atCursor = " "
			after = string(e.buf[cursor:])
		} else {
			atCursor = string(e.buf[cursor])
			after = string(e.buf[cursor+1:])
		}
	}

	// Fake block cursor: emerald background, dark glyph (matches v1).
	cursorCell := ansi.Cursor(atCursor)
	full := before + cursorCell + after

	logicalLines := strings.Split(full, "\n")
	var lines []string
	for _, ll := range logicalLines {
		wrapped := ansi.WrapTextWithAnsi(ll, width)
		lines = append(lines, wrapped...)
	}

	// Find the row containing the cursor. We must count WRAPPED lines, not
	// just newlines: when the user types a long paragraph (no embedded \n),
	// the cursor can be many visual rows down due to word wrapping even
	// though there is only one logical line. Counting only \n would say the
	// cursor is at row 0, the viewport would pin to the top, and the cursor
	// (and the "↑ N more" indicator) would silently disappear.
	//
	// Build the layout up to the cursor position and count those rows.
	beforeWithCursor := before + cursorCell
	beforeLogical := strings.Split(beforeWithCursor, "\n")
	beforeRows := 0
	for _, ll := range beforeLogical {
		wrapped := ansi.WrapTextWithAnsi(ll, width)
		beforeRows += len(wrapped)
	}
	// beforeRows counts wrapped rows of (before + cursorCell). The cursor
	// itself sits at the first wrapped row of that prefix within the last
	// logical line — so the cursor's visual row index is (beforeRows - 1).
	if beforeRows == 0 {
		beforeRows = 1
	}
	cursorRow := beforeRows - 1
	return scrollToCursor(lines, cursorRow, maxEditorRows)
}

// HiddenAbove reports how many wrapped input lines are scrolled off the top of
// the editor's visible window at the given width (0 when everything fits). The
// separator above the editor uses this to show an "↑ N more" hint. It recomputes
// from the current buffer, so it's correct even though the separator renders
// before the editor in the tree.
func (e *Editor) HiddenAbove(width int) int {
	_, above := e.layout(width)
	return above
}

// scrollToCursor returns at most maxRows lines (windowed so cursorRow is
// visible) plus the number of lines hidden above the window.
func scrollToCursor(lines []string, cursorRow, maxRows int) (visible []string, hiddenAbove int) {
	if len(lines) <= maxRows {
		return lines, 0
	}
	start := cursorRow - maxRows + 1
	if start < 0 {
		start = 0
	}
	if start > len(lines)-maxRows {
		start = len(lines) - maxRows
	}
	return lines[start : start+maxRows], start
}

func (e *Editor) changed() {
	if e.OnChange != nil {
		e.OnChange(string(e.buf))
	}
	e.repaint()
}

func (e *Editor) repaint() {
	if e.renderer != nil {
		e.renderer.RequestRender(false)
	}
}
