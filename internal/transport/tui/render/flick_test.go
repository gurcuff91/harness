package render

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
)

type multilineComp struct{ lines []string }

func (c *multilineComp) Render(width int) []string { return c.lines }

func TestStreamingMultilineNoFullRepaint(t *testing.T) {
	tui, term := newTestTUI(80, 40)
	comp := &multilineComp{}
	tui.AddChild(comp)

	comp.lines = []string{"line one", "line two", "line three"}
	tui.doRender()

	base := len(term.writes)

	// The real flick pattern: in a SINGLE render, the last previous line
	// changes AND new lines are appended (thinking wraps + grows at once).
	steps := [][]string{
		// last line "line three" → "line three MORE" + new line appended together
		{"line one", "line two", "line three MORE", "line four"},
		{"line one", "line two", "line three MORE", "line four X", "line five"},
		{"line one", "line two", "line three MORE", "line four X", "line five Y", "line six"},
	}
	for _, s := range steps {
		comp.lines = s
		tui.doRender()
	}

	fullClears, relClears := 0, 0
	for _, w := range term.writes[base:] {
		if strings.Contains(w, ansi.FullClear) {
			fullClears++
		} else if strings.Contains(w, ansi.ClearFromCursor) {
			relClears++
		}
	}
	t.Logf("combined grow+append: fullClears=%d relClears=%d (relClears>0 = flick)", fullClears, relClears)
	if relClears > 0 {
		t.Errorf("FLICK: streaming grow+append triggered %d full repaints", relClears)
	}
}

// Ensures the table-flush case (change strictly before the last line + append)
// STILL takes the safe full-repaint path — the fix must not regress it.
func TestTableFlushStillRepaints(t *testing.T) {
	tui, term := newTestTUI(80, 40)
	comp := &multilineComp{}
	tui.AddChild(comp)

	// Seed: a blank separator line mid-buffer, then content.
	comp.lines = []string{"intro", "", "tail"}
	tui.doRender()
	base := len(term.writes)

	// Table flush: the mid-buffer blank (index 1, BEFORE last line) becomes a
	// border AND rows are appended — firstChanged=1 < len-1=2 → must repaint.
	comp.lines = []string{"intro", "| a | b |", "| 1 | 2 |", "| 3 | 4 |"}
	tui.doRender()

	repaint := false
	for _, w := range term.writes[base:] {
		if strings.Contains(w, ansi.ClearFromCursor) || strings.Contains(w, ansi.FullClear) {
			repaint = true
		}
	}
	if !repaint {
		t.Error("table flush (change before last line) must still full-repaint")
	}
}
