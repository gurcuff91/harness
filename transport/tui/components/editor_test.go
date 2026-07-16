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
