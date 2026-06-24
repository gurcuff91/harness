package render

import (
	"strings"
	"testing"
)

// captureTerminal records all writes and reports a fixed size. It does not
// emulate cursor movement — tests assert on the raw byte stream and on diff
// state transitions, which is enough to validate the three strategies.
type captureTerminal struct {
	cols, rows int
	writes     []string
}

func (c *captureTerminal) Start(onInput func(string), onResize func()) error { return nil }
func (c *captureTerminal) Stop()                                             {}
func (c *captureTerminal) Write(data string)                                 { c.writes = append(c.writes, data) }
func (c *captureTerminal) Columns() int                                      { return c.cols }
func (c *captureTerminal) Rows() int                                         { return c.rows }
func (c *captureTerminal) MoveBy(int)                                        {}
func (c *captureTerminal) HideCursor()                                       {}
func (c *captureTerminal) ShowCursor()                                       {}
func (c *captureTerminal) ClearLine()                                        {}
func (c *captureTerminal) ClearFromCursor()                                  {}
func (c *captureTerminal) ClearScreen()                                      {}

func (c *captureTerminal) lastWrite() string {
	if len(c.writes) == 0 {
		return ""
	}
	return c.writes[len(c.writes)-1]
}

// staticComponent renders a fixed set of lines.
type staticComponent struct{ lines []string }

func (s *staticComponent) Render(width int) []string { return s.lines }

func newTestTUI(cols, rows int) (*TUI, *captureTerminal) {
	term := &captureTerminal{cols: cols, rows: rows}
	return New(term), term
}

func TestFirstRenderNoClear(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	tui.AddChild(&staticComponent{lines: []string{"line one", "line two"}})

	tui.doRender()

	out := term.lastWrite()
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Errorf("first render missing content: %q", out)
	}
	// First render must NOT clear the screen.
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("first render should not clear screen: %q", out)
	}
	// Must be wrapped in synchronized output.
	if !strings.HasPrefix(out, "\x1b[?2026h") || !strings.HasSuffix(out, "\x1b[?2026l") {
		t.Errorf("frame not wrapped in sync markers: %q", out)
	}
}

func TestNoChangeNoWrite(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	comp := &staticComponent{lines: []string{"stable"}}
	tui.AddChild(comp)

	tui.doRender()
	countAfterFirst := len(term.writes)

	// Render again with identical content — should not write.
	tui.doRender()
	if len(term.writes) != countAfterFirst {
		t.Errorf("identical render wrote again: %d -> %d", countAfterFirst, len(term.writes))
	}
}

func TestIncrementalUpdateOnlyChangedLine(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	comp := &staticComponent{lines: []string{"alpha", "beta", "gamma"}}
	tui.AddChild(comp)

	tui.doRender() // first render

	// Change only the middle line.
	comp.lines = []string{"alpha", "BETA", "gamma"}
	tui.doRender()

	out := term.lastWrite()
	if !strings.Contains(out, "BETA") {
		t.Errorf("update missing changed line: %q", out)
	}
	// Should not full-clear on an incremental update.
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("incremental update should not clear screen: %q", out)
	}
	// Should not re-emit unchanged lines.
	if strings.Contains(out, "alpha") {
		t.Errorf("incremental update re-emitted unchanged line: %q", out)
	}
}

func TestWidthChangeFullRedraw(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	comp := &staticComponent{lines: []string{"content"}}
	tui.AddChild(comp)

	tui.doRender()

	// Simulate terminal resize (width change) → full redraw with clear.
	term.cols = 100
	tui.doRender()

	out := term.lastWrite()
	if !strings.Contains(out, "\x1b[2J") {
		t.Errorf("width change should full-clear: %q", out)
	}
}

func TestAppendedLines(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	comp := &staticComponent{lines: []string{"first"}}
	tui.AddChild(comp)

	tui.doRender()

	comp.lines = []string{"first", "second", "third"}
	tui.doRender()

	out := term.lastWrite()
	if !strings.Contains(out, "second") || !strings.Contains(out, "third") {
		t.Errorf("appended lines missing: %q", out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("appending should not full-clear: %q", out)
	}
}

func TestRemovedLinesClear(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	comp := &staticComponent{lines: []string{"a", "b", "c", "d"}}
	tui.AddChild(comp)
	tui.doRender()

	// Shrink content — fewer lines than max → full redraw clears stale rows.
	comp.lines = []string{"a", "b"}
	tui.doRender()

	out := term.lastWrite()
	// Either a full clear, or trailing-line clears must be emitted.
	if !strings.Contains(out, "\x1b[2J") && !strings.Contains(out, "\x1b[2K") {
		t.Errorf("removed lines not cleared: %q", out)
	}
}

func TestSanitizeOverWideLines(t *testing.T) {
	tui, term := newTestTUI(10, 24)
	// Line exceeds width — must be clipped, never crash the diff.
	tui.AddChild(&staticComponent{lines: []string{"this line is way too long for ten cols"}})

	tui.doRender()

	out := term.lastWrite()
	// Strip sync markers and inspect the content line.
	content := strings.TrimPrefix(out, "\x1b[?2026h")
	content = strings.TrimSuffix(content, "\x1b[?2026l")
	// The visible portion must not exceed 10 columns.
	if vw := visibleLen(content); vw > 10 {
		t.Errorf("over-wide line not clipped: visible width %d in %q", vw, content)
	}
}

// visibleLen counts non-escape characters for the assertion above.
func visibleLen(s string) int {
	n := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			// Skip a CSI sequence.
			j := i + 2
			for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
				j++
			}
			i = j + 1
			continue
		}
		if s[i] == '\r' || s[i] == '\n' {
			i++
			continue
		}
		n++
		i++
	}
	return n
}
