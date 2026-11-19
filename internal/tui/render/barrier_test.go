package render

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// The barrier is only worth anything if it sits on the path to the terminal: these tests look at
// the bytes doRender WRITES, not at the helper in isolation.

// payloads are sequences a terminal interprets, as a hostile text would carry them.
var payloads = map[string]string{
	"OSC 52 clipboard": "\x1b]52;c;UFdOLUNMSVA=\x07",
	"OSC 0 title":      "\x1b]0;PWN\x07",
	"CSI 6n query":     "\x1b[6n",
	"alt screen":       "\x1b[?1049h",
	"DCS":              "\x1b" + "Pq#PWN\x1b\\",
	"APC":              "\x1b_PWN\x1b\\",
	"RIS":              "\x1bc",
	"C1 CSI":           "\u009b6n",
	"erase screen":     "\x1b[2J",
	"conceal":          "\x1b[8m",
}

// interpretable reports whether s carries anything but the sequences the TUI emits itself: the
// frame wrapper (synchronized output), cursor movement and line erase between lines — which the
// RENDERER writes, never a component — and the allowed SGR / hyperlinks. It strips what the
// renderer itself adds and checks the rest with the same notion of "allowed" the barrier uses.
func interpretable(w string) bool {
	for _, own := range []string{ansi.SyncBegin, ansi.SyncEnd, ansi.HideCursor, ansi.ShowCursor, ansi.ClearLine, ansi.ClearFromCursor, ansi.FullClear, ansi.ClearScreenHome, ansi.ClearScrollback, ansi.CRLF, ansi.CR} {
		w = strings.ReplaceAll(w, own, "")
	}
	// cursor moves the renderer emits: ESC [ n A / ESC [ n B
	for i := 0; i < len(w); {
		if w[i] == 0x1b && i+1 < len(w) && w[i+1] == '[' {
			j := i + 2
			for j < len(w) && w[j] >= '0' && w[j] <= '9' {
				j++
			}
			if j < len(w) && (w[j] == 'A' || w[j] == 'B') && j > i+2 {
				w = w[:i] + w[j+1:]
				continue
			}
		}
		i++
	}
	return ansi.SafeLine(w) != w
}

func written(term *captureTerminal) string { return strings.Join(term.writes, "") }

// forceRedraw makes the next doRender a full clear-and-redraw, the way RequestRender(true) does —
// without RequestRender's side effect of scheduling a timer-driven frame. That frame runs on another
// goroutine and reads the component while a test is changing its lines, which is a race of the
// test's own making (the renderer serializes this in production with the TUI's lock).
func forceRedraw(tui *TUI) {
	tui.mu.Lock()
	tui.sanitizer.reset()
	tui.previousLines = nil
	tui.previousWidth = -1
	tui.previousHeight = -1
	tui.cursorRow = 0
	tui.hardwareCursorRow = 0
	tui.maxLinesRendered = 0
	tui.previousViewportTop = 0
	tui.mu.Unlock()
}

func TestNothingInterpretableReachesTheTerminalOnTheFirstRender(t *testing.T) {
	for name, p := range payloads {
		tui, term := newTestTUI(80, 24)
		tui.AddChild(&staticComponent{lines: []string{"hello " + p + " world", "second"}})
		tui.doRender()
		out := written(term)
		if interpretable(out) {
			t.Errorf("%s: an interpretable sequence reached the terminal: %q", name, out)
		}
		if !strings.Contains(out, "hello ") || !strings.Contains(out, " world") {
			t.Errorf("%s: the surrounding text is gone: %q", name, out)
		}
	}
}

func TestNothingInterpretableReachesTheTerminalOnADiffRender(t *testing.T) {
	for name, p := range payloads {
		tui, term := newTestTUI(80, 24)
		comp := &staticComponent{lines: []string{"first", "second"}}
		tui.AddChild(comp)
		tui.doRender()
		term.writes = nil
		comp.lines = []string{"first", "second " + p, "third " + p} // changed + appended: the diff path
		tui.doRender()
		if len(term.writes) == 0 {
			t.Fatalf("%s: nothing was drawn", name)
		}
		if out := written(term); interpretable(out) {
			t.Errorf("%s: an interpretable sequence reached the terminal on a diff render: %q", name, out)
		}
	}
}

func TestNothingInterpretableReachesTheTerminalOnAFullRedraw(t *testing.T) {
	for name, p := range payloads {
		tui, term := newTestTUI(80, 24)
		comp := &staticComponent{lines: []string{"a " + p, "b"}}
		tui.AddChild(comp)
		tui.doRender()
		term.writes = nil
		forceRedraw(tui) // forced: resets the cache and redraws everything
		tui.doRender()
		if len(term.writes) == 0 {
			t.Fatalf("%s: nothing was drawn", name)
		}
		if out := written(term); interpretable(out) {
			t.Errorf("%s: an interpretable sequence reached the terminal on a full redraw: %q", name, out)
		}
	}
}

func TestNothingInterpretableReachesTheTerminalAfterAResize(t *testing.T) {
	for name, p := range payloads {
		tui, term := newTestTUI(80, 24)
		tui.AddChild(&staticComponent{lines: []string{"a " + p, "b"}})
		tui.doRender()
		term.writes = nil
		term.cols = 60 // width change: the absolute full-redraw strategy
		tui.doRender()
		if out := written(term); interpretable(out) {
			t.Errorf("%s: an interpretable sequence reached the terminal after a resize: %q", name, out)
		}
	}
}

// The cache must not remember a line as clean: the SAME hostile line reappearing later, or a clean
// line turning hostile, is neutralized every time.
func TestTheLineCacheNeverLetsAHostileLineThrough(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	comp := &staticComponent{lines: []string{"clean line", "clean two"}}
	tui.AddChild(comp)
	tui.doRender()
	for i := 0; i < 3; i++ {
		term.writes = nil
		comp.lines = []string{"clean line", "was clean, now \x1b]52;c;UFdO\x07 hostile", "x"}
		tui.doRender()
		if out := written(term); interpretable(out) {
			t.Fatalf("round %d: a line that turned hostile got through: %q", i, out)
		}
		comp.lines = []string{"clean line", "clean two", "x"} // and back
		tui.doRender()
	}
}

func TestSanitizeLinesAndTheCachedSanitizerAgreeOnHostileInput(t *testing.T) {
	const width = 30
	var lines []string
	for _, p := range payloads {
		lines = append(lines, "x "+p+" y", p, strings.Repeat("wide ", 12)+p, "\x1b[1mbold\x1b[0m "+p)
	}
	var s lineSanitizer
	for round := 0; round < 3; round++ {
		want := sanitizeLines(append([]string(nil), lines...), width)
		got := s.sanitize(append([]string(nil), lines...), width)
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("round %d line %d: cached %q, stateless %q", round, i, got[i], want[i])
			}
			if ansi.VisibleWidth(got[i]) > width {
				t.Errorf("line %d is wider than %d after the barrier: %q", i, width, got[i])
			}
			if ansi.SafeLine(got[i]) != got[i] {
				t.Errorf("line %d is not a fixed point of the barrier: %q", i, got[i])
			}
		}
	}
}

// Neutralizing happens BEFORE measuring and clipping: a visible "␛" is one column wide, and the
// clip must not cut an allowed sequence in half or leave a dangling hyperlink.
func TestClippingAfterTheBarrierKeepsTheLineWellFormed(t *testing.T) {
	line := "\x1b]52;c;" + strings.Repeat("A", 200) + "\x07 tail"
	out := sanitizeLines([]string{line}, 20)[0]
	if ansi.VisibleWidth(out) > 20 {
		t.Errorf("wider than the terminal: %d", ansi.VisibleWidth(out))
	}
	if strings.ContainsRune(out, 0x1b) && ansi.SafeLine(out) != out {
		t.Errorf("the clipped line carries a live escape: %q", out)
	}
	if strings.Contains(out, "\x07") {
		t.Errorf("a live BEL survived: %q", out)
	}
}

// Neutralizing makes text VISIBLE ("␛" and "␇" take a column each), so it can make a line wider
// than it measured before: the barrier must run BEFORE the width is measured and clipped, or a line
// would leave the sanitizer wider than the terminal and corrupt the diff state. Checked through the
// cached path doRender really takes, on what is finally written.
func TestEveryWrittenLineFitsTheTerminalAfterTheBarrier(t *testing.T) {
	const cols = 20
	for name, p := range payloads {
		tui, term := newTestTUI(cols, 24)
		// the sequence is recognized as ZERO-width before neutralizing and wide after it
		tui.AddChild(&staticComponent{lines: []string{p + p + p + p, "ok " + p}})
		tui.doRender()
		for _, w := range term.writes {
			for _, l := range strings.Split(w, "\n") {
				l = strings.NewReplacer(ansi.SyncBegin, "", ansi.SyncEnd, "", ansi.ClearLine, "").Replace(strings.TrimRight(l, "\r"))
				if vw := ansi.VisibleWidth(l); vw > cols {
					t.Errorf("%s: a written line is %d columns wide on a %d-column terminal: %q", name, vw, cols, l)
				}
			}
		}
	}
}

// A hostile line that stays the same from one frame to the next is processed once and cached — and
// the cached copy must be the NEUTRALIZED one.
func TestACachedHostileLineStaysNeutralized(t *testing.T) {
	tui, term := newTestTUI(80, 24)
	comp := &staticComponent{lines: []string{"stable \x1b]52;c;UFdO\x07 line", "volatile 0"}}
	tui.AddChild(comp)
	tui.doRender()
	for i := 1; i < 4; i++ {
		term.writes = nil
		comp.lines[1] = "volatile " + string(rune('0'+i)) // only the second line changes; the first comes from the cache
		tui.doRender()
		forceRedraw(tui)
		tui.doRender()
		if out := written(term); interpretable(out) {
			t.Fatalf("frame %d: the cached line was written raw: %q", i, out)
		}
	}
}

// No TAB reaches the terminal, through the real render path (first frame, a
// diff and a cached line): a terminal draws it to the next 8-column stop, wider
// than the renderer measured it, and the rows below drift (the editor's border
// was seen smeared into a Bash tool's body whose command had tabs in it).
func TestNoTabReachesTheTerminal(t *testing.T) {
	const cols = 40
	tui, term := newTestTUI(cols, 24)
	line := "('\t\t\tif requestedMaxIterations > 0 {"
	comp := &staticComponent{lines: []string{line, "second"}}
	tui.AddChild(comp)
	tui.doRender()
	comp.lines = []string{line, "second\tchanged"}
	tui.doRender()
	out := written(term)
	if strings.ContainsRune(out, '\t') {
		t.Fatalf("a TAB reached the terminal: %q", out)
	}
	if !strings.Contains(out, "('         if requestedMaxIterations") {
		t.Errorf("the tabs were not drawn as spaces: %q", out)
	}
}
