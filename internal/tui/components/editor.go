package components

import (
	"strings"
	"sync"

	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/keys"
)

// maxEditorRows caps the rendered height of the editor (matches v1's 5-line cap).
const maxEditorRows = 5

// Editor is a multi-line text input with a fake block cursor, placeholder text,
// word-wise navigation, and paste handling. Port of the harness's v1 input
// (transport/tui), built on the Component model.
//
// Newline insertion uses Ctrl+J (LF) or Alt+Enter (both reliable without the
// Kitty protocol — see the TODO in term/terminal.go). Enter (\r) submits.
//
// It is safe for concurrent use, because it IS used from several goroutines: the input goroutine
// (every key), the render timer's goroutine (Render, and HiddenAbove through the separator above
// it) and, now and then, a helper goroutine (a clipboard image paste inserting a path). The rules
// are the same as SelectList's:
//
//   - buf, cursor and placeholder are guarded by mu;
//   - Render/HiddenAbove take ONE snapshot under the lock and lay out outside it, so they never
//     see a buffer and a cursor from two different moments;
//   - no callback runs while mu is held. OnChange re-enters the editor (the palette's push calls
//     Clear, its Tab calls SetValue, and it reads Value), and OnSubmit/OnEscape run whole commands.
//     Everything an edit wants to tell the outside world is collected in act while the lock is
//     held and delivered by deliver after it is released.
type Editor struct {
	mu          sync.Mutex
	buf         []rune
	cursor      int // rune index into buf
	placeholder string

	// act is what the edit in progress wants delivered once mu is released (guarded by mu; taken
	// and reset by take).
	act editorAct

	// Callbacks.
	OnSubmit func(text string)
	OnChange func(text string)
	OnEscape func()

	// ColorFn, when set, overrides the default text and cursor color. It should
	// return a hex color string (e.g. ansi.HexWarn). nil = default teal palette.
	ColorFn func() string

	// DisableSubmit, when true, makes Enter insert a newline instead of
	// submitting (used while an overlay/palette owns the Enter key).
	DisableSubmit bool

	renderer Renderer
}

// editorAct is the deferred outside-world effects of one edit: callbacks and repaints.
type editorAct struct {
	changed bool   // OnChange(text) is due
	repaint bool   // a repaint is due (implied by changed)
	submit  bool   // OnSubmit(text) is due
	escape  bool   // OnEscape() is due
	text    string // the buffer as it was when the edit finished
}

// take returns the pending effects with the text they refer to, and resets them. Caller holds mu.
func (e *Editor) take() editorAct {
	a := e.act
	e.act = editorAct{}
	if a.changed || a.submit {
		a.text = string(e.buf)
	}
	return a
}

// deliver runs the effects of one edit, in the order the editor always used: OnSubmit or
// OnEscape, else OnChange then the repaint. Never call it with mu held.
func (e *Editor) deliver(a editorAct) {
	switch {
	case a.submit:
		if e.OnSubmit != nil {
			e.OnSubmit(a.text)
		}
		return
	case a.escape:
		if e.OnEscape != nil {
			e.OnEscape()
		}
		return
	}
	if a.changed && e.OnChange != nil {
		e.OnChange(a.text)
	}
	if (a.changed || a.repaint) && e.renderer != nil {
		e.renderer.RequestRender(false)
	}
}

// NewEditor creates an editor bound to a renderer (for repaint on edits).
func NewEditor(r Renderer, placeholder string) *Editor {
	return &Editor{renderer: r, placeholder: placeholder}
}

// Value returns the current text.
func (e *Editor) Value() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.buf)
}

// SetValue replaces the text and moves the cursor to the end.
func (e *Editor) SetValue(s string) {
	e.mu.Lock()
	e.buf = []rune(s)
	e.cursor = len(e.buf)
	e.act.changed = true
	a := e.take()
	e.mu.Unlock()
	e.deliver(a)
}

// InsertText inserts s at the cursor position (e.g. a pasted image path). When
// text already precedes the cursor, a separating space is added so the inserted
// token doesn't fuse with the previous word.
func (e *Editor) InsertText(s string) {
	e.mu.Lock()
	if e.cursor > 0 && e.buf[e.cursor-1] != ' ' && e.buf[e.cursor-1] != '\n' {
		s = " " + s
	}
	e.insert(s)
	a := e.take()
	e.mu.Unlock()
	e.deliver(a)
}

// Clear empties the editor.
func (e *Editor) Clear() {
	e.mu.Lock()
	e.buf = nil
	e.cursor = 0
	e.act.changed = true
	a := e.take()
	e.mu.Unlock()
	e.deliver(a)
}

// SetPlaceholder changes the dim hint shown when the editor is empty.
func (e *Editor) SetPlaceholder(s string) {
	e.mu.Lock()
	e.placeholder = s
	e.mu.Unlock()
}

// Placeholder returns the current placeholder text.
func (e *Editor) Placeholder() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.placeholder
}

// CursorPos returns the rune index of the cursor.
func (e *Editor) CursorPos() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursor
}

// HandleInput processes a single input sequence. The edit happens under the lock; the callbacks
// it caused run after it is released (see Editor).
func (e *Editor) HandleInput(data string) {
	e.mu.Lock()
	e.handle(data)
	a := e.take()
	e.mu.Unlock()
	e.deliver(a)
}

// handle applies one input sequence to the buffer. Caller holds mu; nothing here may call a
// callback or a public method — it records what is due in e.act instead.
func (e *Editor) handle(data string) {
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
			e.act.submit = true
			return
		case keys.AltEnter, keys.CtrlJ:
			// Insert a newline. Ctrl+J (LF) is the reliable terminal newline;
			// Alt+Enter is kept as an alternative. Shift+Enter can't be told
			// apart from Enter without the Kitty keyboard protocol.
			e.insert("\n")
			return
		case keys.Escape:
			e.act.escape = true
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
	visible, _ := e.layout(width)
	return visible
}

// layout computes the editor's wrapped, cursor-windowed visible lines and how
// many lines are scrolled off the top. It is pure (no stored state), so callers
// — including the separator above the editor — always get a value consistent
// with the CURRENT buffer, independent of child render order.
func (e *Editor) layout(width int) (visible []string, hiddenAbove int) {
	// ONE snapshot under the lock; everything below works on the copy, never on e.buf/e.cursor.
	e.mu.Lock()
	buf := append([]rune(nil), e.buf...)
	cursor := e.cursor
	placeholder := e.placeholder
	e.mu.Unlock()

	if len(buf) == 0 {
		return []string{ansi.Dimmed(placeholder)}, 0
	}

	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(buf) {
		cursor = len(buf)
	}

	// Resolve active color (default = primary teal).
	hexColor := ansi.HexPrimary
	if e.ColorFn != nil {
		hexColor = e.ColorFn()
	}

	before := string(buf[:cursor])
	if hexColor != ansi.HexPrimary {
		before = ansi.FG(hexColor, before)
	}
	atCursor := " "
	after := ""
	if cursor < len(buf) {
		if buf[cursor] == '\n' {
			atCursor = " "
			after = string(buf[cursor:])
		} else {
			atCursor = string(buf[cursor])
			after = string(buf[cursor+1:])
		}
	}
	if hexColor != ansi.HexPrimary && after != "" {
		after = ansi.FG(hexColor, after)
	}

	// Block cursor: background = active color, dark foreground glyph.
	cursorCell := ansi.CursorColored(atCursor, hexColor)
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

// changed records that the text changed: OnChange and a repaint are due once mu is released.
func (e *Editor) changed() { e.act.changed = true }

// repaint records that only the cursor moved: a repaint is due once mu is released.
func (e *Editor) repaint() { e.act.repaint = true }
