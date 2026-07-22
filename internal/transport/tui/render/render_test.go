package render

import (
	"strconv"
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
	// Width change forces a full redraw. Inline TUI prefers a relative clear
	// (\x1b[J, scrollback-safe) when the block is on-screen; an absolute clear
	// (\x1b[2J) is acceptable when content scrolled off the top.
	if !strings.Contains(out, "\x1b[2J") && !strings.Contains(out, "\x1b[J") {
		t.Errorf("width change should redraw with a clear: %q", out)
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
	// Acceptable: absolute clear (\x1b[2J), per-line clear (\x1b[2K), or the
	// scrollback-safe relative clear-to-end (\x1b[J).
	if !strings.Contains(out, "\x1b[2J") && !strings.Contains(out, "\x1b[2K") && !strings.Contains(out, "\x1b[J") {
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

// TestScrollOffsetRepaintsFromTop verifies that when the user has scrolled
// up (scrollOffset > 0), doRender uses renderFromTop instead of the
// bottom-sticking incremental path, preserving the manual viewport.
func TestScrollOffsetRepaintsFromTop(t *testing.T) {
	tui, term := newTestTUI(20, 5)
	// 10 content lines, terminal is 5 rows tall.
	lines := []string{"L0", "L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8", "L9"}
	tui.AddChild(&staticComponent{lines: lines})

	// First render: bottom of content visible.
	tui.doRender()
	if tui.previousViewportTop != 5 {
		t.Fatalf("first render viewport top got %d want 5", tui.previousViewportTop)
	}

	// Scroll up 3 lines.
	tui.SetScrollOffset(3)

	// The next render must repaint from top=2 (5-3), not from top=5.
	tui.doRender()
	if tui.previousViewportTop != 2 {
		t.Errorf("scrolled render viewport top got %d want 2", tui.previousViewportTop)
	}

	out := term.lastWrite()
	// Should contain L2..L6 (topRow 2, height 5).
	for i := 2; i <= 6; i++ {
		if !strings.Contains(out, "L"+strconv.Itoa(i)) {
			t.Errorf("scrolled render missing L%d: %q", i, out)
		}
	}
	// Must NOT contain L9 (still below visible area).
	if strings.Contains(out, "L9") {
		t.Errorf("scrolled render should not contain L9: %q", out)
	}
}

// TestScrollOffsetZeroSticksToBottom verifies that scrollOffset == 0 keeps
// the existing bottom-sticking behavior after content grows.
func TestScrollOffsetZeroSticksToBottom(t *testing.T) {
	tui, term := newTestTUI(20, 5)
	tui.AddChild(&staticComponent{lines: []string{"L0", "L1", "L2", "L3", "L4"}})

	tui.doRender()
	if tui.previousViewportTop != 0 {
		t.Fatalf("initial viewport top got %d want 0", tui.previousViewportTop)
	}

	// Grow beyond terminal height.
	tui.children[0] = &staticComponent{lines: []string{"L0", "L1", "L2", "L3", "L4", "L5", "L6", "L7"}}
	tui.doRender()
	if tui.previousViewportTop != 3 {
		t.Errorf("bottom-stick viewport top got %d want 3", tui.previousViewportTop)
	}

	// Must not have used a full clear (incremental path expected).
	out := term.lastWrite()
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("bottom-stick growth should not full-clear: %q", out)
	}
}

// TestScrollReturnToBottomForcesRedraw verifies that the transition from a
// manual scroll (scrollOffset > 0) back to stick-to-bottom issues a full
// relative redraw and recomputes the viewport from the end. An earlier version
// reused the scrolled viewport top and ended up corrupting the input/footer
// area.
func TestScrollReturnToBottomForcesRedraw(t *testing.T) {
	tui, term := newTestTUI(20, 5)
	lines := []string{"L0", "L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8", "L9"}
	tui.AddChild(&staticComponent{lines: lines})

	// First render: bottom of content visible (viewport top 5).
	tui.doRender()
	if tui.previousViewportTop != 5 {
		t.Fatalf("first render viewport top got %d want 5", tui.previousViewportTop)
	}

	// Scroll up 3 lines (viewport top should be 2).
	tui.SetScrollOffset(3)
	tui.doRender()
	if tui.previousViewportTop != 2 {
		t.Fatalf("scrolled viewport top got %d want 2", tui.previousViewportTop)
	}

	// Return to bottom. Must force a redraw and re-anchor to the end.
	tui.SetScrollOffset(0)
	tui.doRender()
	if tui.previousViewportTop != 5 {
		t.Errorf("return-to-bottom viewport top got %d want 5", tui.previousViewportTop)
	}

	out := term.lastWrite()
	// A full redraw is required on this transition. We clear the active
	// screen and repaint from home (preserving shell scrollback), not the
	// incremental bottom-stick path that would reuse the scrolled viewport.
	if !strings.Contains(out, "\x1b[2J") {
		t.Errorf("return-to-bottom render should clear active screen: %q", out)
	}
	if strings.Contains(out, "\x1b[3J") {
		// \x1b[3J erases scrollback; we want to preserve it on this transition.
		t.Errorf("return-to-bottom render should not erase scrollback: %q", out)
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
