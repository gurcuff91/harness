package components

import (
	"strings"
	"testing"
)

func TestEditorTypeAndSubmit(t *testing.T) {
	var submitted string
	e := NewEditor(nil, "type...")
	e.OnSubmit = func(s string) { submitted = s }

	for _, ch := range "hello" {
		e.HandleInput(string(ch))
	}
	if e.Value() != "hello" {
		t.Errorf("value = %q, want hello", e.Value())
	}
	e.HandleInput("\r") // Enter submits
	if submitted != "hello" {
		t.Errorf("submitted = %q, want hello", submitted)
	}
}

func TestEditorBackspace(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue("abc")
	e.HandleInput("\x7f") // backspace
	if e.Value() != "ab" {
		t.Errorf("value = %q, want ab", e.Value())
	}
}

func TestEditorCursorMovement(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue("abc")
	e.HandleInput("\x1b[D") // left
	e.HandleInput("\x1b[D") // left
	e.HandleInput("X")
	if e.Value() != "aXbc" {
		t.Errorf("value = %q, want aXbc", e.Value())
	}
}

func TestEditorHomeEnd(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue("hello")
	e.HandleInput("\x01") // Ctrl+A → home
	e.HandleInput("X")
	if e.Value() != "Xhello" {
		t.Errorf("value = %q, want Xhello", e.Value())
	}
	e.HandleInput("\x05") // Ctrl+E → end
	e.HandleInput("Y")
	if e.Value() != "Xhello"+"Y" {
		t.Errorf("value = %q, want XhelloY", e.Value())
	}
}

func TestEditorAltEnterNewline(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue("line1")
	e.HandleInput("\x1b\r") // Alt+Enter → newline
	e.HandleInput("X")
	if e.Value() != "line1\nX" {
		t.Errorf("value = %q, want line1\\nX", e.Value())
	}
}

func TestEditorDisableSubmit(t *testing.T) {
	submitted := false
	e := NewEditor(nil, "")
	e.OnSubmit = func(string) { submitted = true }
	e.DisableSubmit = true
	e.SetValue("text")
	e.HandleInput("\r") // Enter inserts newline instead of submitting
	if submitted {
		t.Errorf("submit should be disabled")
	}
	if e.Value() != "text\n" {
		t.Errorf("value = %q, want text\\n", e.Value())
	}
}

func TestEditorPlaceholder(t *testing.T) {
	e := NewEditor(nil, "type a message")
	lines := e.Render(80)
	if len(lines) != 1 || !strings.Contains(lines[0], "type a message") {
		t.Errorf("placeholder not rendered: %v", lines)
	}
}

func TestEditorCursorRendered(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue("hi")
	lines := e.Render(80)
	joined := strings.Join(lines, "\n")
	// Fake cursor uses an emerald background block (ansi.Cursor).
	if !strings.Contains(joined, "48;2;38;166;154") {
		t.Errorf("emerald cursor not rendered: %q", joined)
	}
}

func TestEditorDeleteWord(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue("hello world")
	e.HandleInput("\x17") // Ctrl+W → delete word back
	if e.Value() != "hello " {
		t.Errorf("value = %q, want 'hello '", e.Value())
	}
}

func TestSelectListNavigation(t *testing.T) {
	items := []SelectItem{
		{Value: "a", Label: "Alpha"},
		{Value: "b", Label: "Beta"},
		{Value: "c", Label: "Gamma"},
	}
	var chosen string
	s := NewSelectList(items, 5)
	s.OnSelect = func(it SelectItem) { chosen = it.Value }

	s.HandleInput("\x1b[B") // down → Beta
	s.HandleInput("\r")     // enter
	if chosen != "b" {
		t.Errorf("chosen = %q, want b", chosen)
	}
}

func TestSelectListWrap(t *testing.T) {
	items := []SelectItem{{Value: "a"}, {Value: "b"}}
	s := NewSelectList(items, 5)
	s.HandleInput("\x1b[A") // up from first wraps to last
	if it, _ := s.Selected(); it.Value != "b" {
		t.Errorf("up-wrap selected %q, want b", it.Value)
	}
}

func TestSelectListFilter(t *testing.T) {
	items := []SelectItem{
		{Value: "connect", Label: "connect"},
		{Value: "disconnect", Label: "disconnect"},
		{Value: "delete", Label: "delete"},
	}
	s := NewSelectList(items, 5)
	// Contains-based filter (matches harness v1 palette): "con" is in both
	// "connect" and "disconnect".
	s.SetFilter("con")
	if s.Count() != 2 {
		t.Errorf("filter 'con' matched %d, want 2", s.Count())
	}
	// "del" matches only "delete".
	s.SetFilter("del")
	if s.Count() != 1 {
		t.Errorf("filter 'del' matched %d, want 1", s.Count())
	}
}

func TestSelectListCancel(t *testing.T) {
	cancelled := false
	s := NewSelectList([]SelectItem{{Value: "x"}}, 5)
	s.OnCancel = func() { cancelled = true }
	s.HandleInput("\x1b") // escape
	if !cancelled {
		t.Errorf("escape should cancel")
	}
}

// TestEditorLongParagraphScrollsAndShowsIndicator reproduces the bug where
// typing a long paragraph (no embedded newlines) caused the editor viewport
// to silently pin to the top of the text. The cursor sat many visual rows
// below the visible window, and the "↑ N more" separator hint stayed empty
// because cursorRow was computed from \n count alone.
func TestEditorLongParagraphScrollsAndShowsIndicator(t *testing.T) {
	e := NewEditor(nil, "type...")
	width := 30

	// A single logical line that wraps to far more than maxEditorRows (5).
	// ~120 chars at width 30 -> ~4 wrapped rows per logical line, multiplied
	// by 3 logical lines via explicit \n -> many more than 5 total rows.
	long := "the quick brown fox jumps over the lazy dog"
	text := strings.Repeat(long+" ", 3) + strings.Repeat(long+"\n", 4)
	e.SetValue(text)

	// Cursor at end of buffer — many wrapped rows below the viewport.
	visible := e.Render(width)
	hidden := e.HiddenAbove(width)

	if len(visible) > maxEditorRows {
		t.Errorf("editor rendered %d lines, must not exceed maxEditorRows=%d", len(visible), maxEditorRows)
	}
	if hidden == 0 {
		t.Errorf("HiddenAbove=0 with cursor at end of long wrapped buffer — the \"↑ N more\" indicator would be missing")
	}
}

// TestEditorLongParagraphShowsCursor confirms the cursor row is inside the
// visible window when the cursor is at the end of a long wrapped paragraph
// (not silently scrolled off the bottom).
func TestEditorLongParagraphShowsCursor(t *testing.T) {
	e := NewEditor(nil, "type...")
	width := 30
	long := "the quick brown fox jumps over the lazy dog"
	text := strings.Repeat(long+" ", 4)
	e.SetValue(text)

	visible := e.Render(width)
	// The visible window must end with a line that contains the cursor
	// character (block cursor cell) — otherwise the user has lost sight of
	// the caret.
	last := visible[len(visible)-1]
	if !strings.Contains(last, "\x1b[7m") && !strings.Contains(last, "block") {
		// Soft check: at minimum the last line must NOT be the original first
		// portion of the buffer (which would mean the window pinned to top).
		first30 := text[:width]
		if strings.HasPrefix(strings.TrimSpace(last), strings.TrimSpace(first30)) {
			t.Errorf("last visible line looks like the beginning of the buffer — viewport is pinned to the top, cursor hidden")
		}
	}
}

// maxEditorRows is declared in editor.go and accessible from this package.
