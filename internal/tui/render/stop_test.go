package render

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// TestStopParksCursorOneLineBelowContent pins down an off-by-one in Stop():
// it must leave the cursor on the line immediately after the last rendered
// line — exactly one step down — so a caller printing right after (the TUI's
// "👋 Goodbye!" farewell) gets the single blank line of separation its own
// comment assumes.
//
// The bug: Stop() moved to row `prevLen` (already one past the content, since
// the last line is at index prevLen-1) and THEN wrote a CRLF, landing two
// lines below the content and rendering the farewell with an extra blank line
// above it. The fix targets prevLen-1 and lets the CRLF do the final step.
func TestStopParksCursorOneLineBelowContent(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	// 5 lines of content, comfortably inside the 24-row terminal so no
	// scrolling is involved and the cursor math is unambiguous.
	tui.AddChild(&staticComponent{lines: []string{"L0", "L1", "L2", "L3", "L4"}})
	tui.doRender()

	// After a render the cursor sits on the LAST content line (index 4).
	if tui.hardwareCursorRow != 4 {
		t.Fatalf("setup: hardwareCursorRow = %d, want 4 (last of 5 lines)", tui.hardwareCursorRow)
	}

	base := len(term.writes)
	tui.Stop()

	out := strings.Join(term.writes[base:], "")

	// From the last content line, reaching the line just below it needs the
	// CRLF only — no MoveDown at all. A MoveDown here is the off-by-one.
	if strings.Contains(out, ansi.MoveDown(1)) {
		t.Errorf("Stop() emitted MoveDown(1) on top of the CRLF — that lands two lines below the content, not one.\ngot: %q", out)
	}
	if !strings.Contains(out, ansi.CRLF) {
		t.Errorf("Stop() should emit exactly one CRLF to step onto the line below the content.\ngot: %q", out)
	}
	if n := strings.Count(out, ansi.CRLF); n != 1 {
		t.Errorf("Stop() emitted %d CRLFs, want exactly 1.\ngot: %q", n, out)
	}
}

// TestStopMovesUpWhenCursorIsBelowLastLine covers the other direction: if the
// cursor ended up somewhere past the last content line, Stop() must move UP to
// the last line before its CRLF, still landing exactly one line below the
// content.
func TestStopMovesUpWhenCursorIsBelowLastLine(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	tui.AddChild(&staticComponent{lines: []string{"L0", "L1", "L2"}})
	tui.doRender()

	// Force the cursor two rows past the last content line (index 2).
	tui.mu.Lock()
	tui.hardwareCursorRow = 4
	tui.mu.Unlock()

	base := len(term.writes)
	tui.Stop()
	out := strings.Join(term.writes[base:], "")

	// Needs to climb from row 4 back to row 2 (the last line) = MoveUp(2).
	if !strings.Contains(out, ansi.MoveUp(2)) {
		t.Errorf("Stop() should MoveUp(2) from row 4 to the last content line (row 2).\ngot: %q", out)
	}
}
