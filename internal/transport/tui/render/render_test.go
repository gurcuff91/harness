package render

import (
	"fmt"
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

// TestSticksToBottom verifies the renderer keeps the bottom-sticking
// (tail-follow) behavior after content grows past the terminal height.
func TestSticksToBottom(t *testing.T) {
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

// TestStreamingNoFlickWhenContentGrowsPastWrap models the real-world TUI case
// where a Markdown block is streaming AND a spinner is ticking at ~80ms. When
// the streamed text crosses the wrap point, the history grows by one line and
// everything below it (the spinner, the footer) shifts down. The renderer must
// NOT full-repaint in that case — only an incremental scroll-and-paint is
// needed. A full repaint causes the visible flick the user reported.
//
// Reproduces the regression where the "change before last line" guard fired on
// the shifted spinner lines and triggered a fullRender(clearRelative).
func TestStreamingNoFlickWhenContentGrowsPastWrap(t *testing.T) {
	tui, term := newTestTUI(80, 20)

	// A growing component (the live Markdown block) + a static block after it
	// (the spinner that takes 3 lines: blank + frame + blank). Mimics the real
	// tree where spinner sits directly under the history.
	grow := &growingComponent{}
	// Pre-fill with enough text that the next chunks push past the wrap.
	grow.text = strings.Repeat("x", 70) // 70 chars at width 80 -> 1 line
	spinner := &staticComponent{lines: []string{"", "⠋ Thinking", ""}}
	tui.AddChild(grow)
	tui.AddChild(spinner)

	// Prime render so previousLines is established.
	tui.doRender()
	base := len(term.writes)

	preLines := len(grow.Render(80))
	// Stream chunks. Each chunk must force a wrap boundary eventually.
	for tick := 0; tick < 30; tick++ {
		if tick%2 == 0 {
			// Add ~20 chars each time -> crosses the 80-col boundary on the first tick.
			grow.text += fmt.Sprintf(" chunk%02d_padding_padding", tick)
		}
		tui.doRender()
	}

	postLines := len(grow.Render(80))
	if postLines <= preLines {
		t.Skipf("content did not cross the wrap point (pre=%d post=%d)", preLines, postLines)
	}

	relClears, fullClears := 0, 0
	for _, w := range term.writes[base:] {
		if strings.Contains(w, "\x1b[J") {
			relClears++
		}
		if strings.Contains(w, "\x1b[2J") {
			fullClears++
		}
	}
	if relClears > 0 || fullClears > 0 {
		t.Errorf("FLICK: streaming+growing content triggered %d relative and %d full clears (should be 0)", relClears, fullClears)
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
