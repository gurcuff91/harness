package components

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ── behaviour must not change ───────────────────────────────────────────────

func renderAll(l *SelectList) string {
	out := ""
	for _, w := range []int{20, 45, 100} {
		for _, line := range l.Render(w) {
			out += line + "\n"
		}
		out += "--\n"
	}
	return out
}

// Wrapping, the "No matches" line, the (n/total) indicator and the reset of the selection on a
// filter are exactly what they were; pinned by value so a future change to the lock cannot alter them.
func TestSelectListBehaviourIsUnchanged(t *testing.T) {
	l := NewSelectList(raceItems(12), 5)
	sel := func() string { it, _ := l.Selected(); return it.Value }

	if sel() != "cmd0" || l.Count() != 12 {
		t.Fatalf("start: %s/%d", sel(), l.Count())
	}
	l.HandleInput("\x1b[A") // Up from the first wraps to the LAST
	if sel() != "cmd11" {
		t.Errorf("Up wrap: %s", sel())
	}
	l.HandleInput("\x1b[B") // Down from the last wraps to the FIRST
	if sel() != "cmd0" {
		t.Errorf("Down wrap: %s", sel())
	}
	l.HandleInput("\x1bOB") // application-mode arrows too
	l.HandleInput("\x1bOB")
	if sel() != "cmd2" {
		t.Errorf("SS3 Down x2: %s", sel())
	}
	l.HandleInput("\x1bOA")
	if sel() != "cmd1" {
		t.Errorf("SS3 Up: %s", sel())
	}

	// the scroll indicator (n/total) appears when the list does not fit, and shows the selection
	for i := 0; i < 6; i++ {
		l.HandleInput("\x1b[B")
	}
	got := l.Render(100)
	if len(got) != 6 || !strings.Contains(got[len(got)-1], "(8/12)") {
		t.Errorf("window %q, want 5 rows and an (8/12) indicator", got)
	}
	// no indicator when everything fits
	l.SetFilter("cmd1") // cmd1, cmd10, cmd11
	if rows := l.Render(100); len(rows) != 3 || strings.Contains(strings.Join(rows, ""), "/") {
		t.Errorf("a fitting list shows %q, want 3 rows and no indicator", rows)
	}
	// a filter resets the selection; so does SetItems
	l.HandleInput("\x1b[B")
	l.SetFilter("cmd1")
	if sel() != "cmd1" {
		t.Errorf("filter did not reset the selection: %s", sel())
	}
	l.HandleInput("\x1b[B")
	l.SetItems(raceItems(4))
	if sel() != "cmd0" || l.Count() != 4 {
		t.Errorf("SetItems: %s/%d", sel(), l.Count())
	}
	// No matches
	l.SetFilter("nothing-matches-this")
	if rows := l.Render(100); len(rows) != 1 || !strings.Contains(rows[0], "No matches") || l.Count() != 0 {
		t.Errorf("no matches renders %q", rows)
	}
	if _, ok := l.Selected(); ok {
		t.Error("an empty list has no selection")
	}
	// keys on an empty list do nothing and notify nothing
	n := 0
	l.OnSelectionChange = func(SelectItem) { n++ }
	l.HandleInput("\x1b[A")
	l.HandleInput("\x1b[B")
	if n != 0 {
		t.Errorf("an empty list notified %d changes", n)
	}
	// Enter on an empty list selects nothing; Escape / Ctrl+C cancel
	selected, cancelled := 0, 0
	l.OnSelect = func(SelectItem) { selected++ }
	l.OnCancel = func() { cancelled++ }
	l.HandleInput("\r")
	l.HandleInput("\x1b")
	l.HandleInput("\x03")
	if selected != 0 || cancelled != 2 {
		t.Errorf("Enter on empty selected %d, Escape+Ctrl+C cancelled %d", selected, cancelled)
	}
}

// Enter confirms the highlighted item, after navigating; the selection callback fires once per move.
func TestSelectListEnterAndSelectionChangeCallbacks(t *testing.T) {
	l := NewSelectList(raceItems(5), 8)
	var changes []string
	var chosen []string
	l.OnSelectionChange = func(it SelectItem) { changes = append(changes, it.Value) }
	l.OnSelect = func(it SelectItem) { chosen = append(chosen, it.Value) }
	l.HandleInput("\x1b[B")
	l.HandleInput("\x1b[B")
	l.HandleInput("\x1b[A")
	l.HandleInput("\r")
	if strings.Join(changes, ",") != "cmd1,cmd2,cmd1" || strings.Join(chosen, ",") != "cmd1" {
		t.Errorf("changes %v chosen %v", changes, chosen)
	}
}

// The width of the label column is computed from the SNAPSHOT it is given (never from s.filtered).
func TestPrimaryColumnWidthUsesTheItemsItIsGiven(t *testing.T) {
	short := []SelectItem{{Value: "a"}, {Value: "bb"}}
	long := []SelectItem{{Value: "a"}, {Label: strings.Repeat("x", 80)}}
	if w := primaryColumnWidth(short); w != 2+primaryColGap {
		t.Errorf("short column %d", w)
	}
	if w := primaryColumnWidth(long); w != defaultPrimaryColWidth {
		t.Errorf("a long label is capped at %d, got %d", defaultPrimaryColWidth, w)
	}
	if w := primaryColumnWidth(nil); w != 1 {
		t.Errorf("empty: %d", w)
	}
}

// Clearing the filter ("" — Backspace back to "/") also resets the selection, like any other filter.
func TestSelectListClearingTheFilterResetsTheSelection(t *testing.T) {
	l := NewSelectList(raceItems(20), 5)
	for i := 0; i < 7; i++ {
		l.HandleInput("\x1b[B")
	}
	if it, _ := l.Selected(); it.Value != "cmd7" {
		t.Fatalf("setup: %s", it.Value)
	}
	l.SetFilter("")
	if it, ok := l.Selected(); !ok || it.Value != "cmd0" {
		t.Errorf("after SetFilter(\"\") the selection is %q, want the first entry", it.Value)
	}
	if rows := l.Render(100); !strings.Contains(strings.Join(rows, "\n"), "→ cmd0") {
		t.Errorf("the window does not show the reset selection: %q", rows)
	}
}

// Render draws OUTSIDE the lock. If it held it, a slow draw would stall the input goroutine (typing
// would lag) for as long as a frame takes — the point of drawing from a snapshot.
//
// The probe uses the cheapest call that needs the lock, Count(), so the time it measures is waiting
// for the lock and nothing else (SetFilter does real work of its own). Render is held in a slow draw
// — thousands of long CJK rows — and Count must still return promptly.
func TestSelectListRenderDoesNotHoldTheLockWhileDrawing(t *testing.T) {
	huge := make([]SelectItem, 3000)
	for i := range huge {
		huge[i] = SelectItem{Value: fmt.Sprintf("c%d", i), Label: strings.Repeat("日本語", 400), Description: strings.Repeat("d", 2000)}
	}
	l := NewSelectList(huge, 3000) // draw all 3000 rows: a deliberately slow Render

	// how long Count() takes with nobody else around: the floor
	floor := time.Duration(1 << 62)
	for i := 0; i < 50; i++ {
		s := time.Now()
		l.Count()
		if d := time.Since(s); d < floor {
			floor = d
		}
	}

	renderDone := make(chan struct{})
	go func() { l.Render(100); close(renderDone) }()
	time.Sleep(5 * time.Millisecond) // past the snapshot, deep in the drawing
	select {
	case <-renderDone:
		t.Skip("Render finished before the probe could observe it: this machine is too fast for the probe")
	default:
	}
	var worst time.Duration
	for i := 0; i < 20; i++ {
		s := time.Now()
		l.Count()
		if d := time.Since(s); d > worst {
			worst = d
		}
		select {
		case <-renderDone:
			i = 20
		default:
		}
	}
	<-renderDone
	if worst > floor+20*time.Millisecond {
		t.Errorf("Count() waited %v (floor %v) while Render was drawing: Render holds the lock", worst, floor)
	}
}
