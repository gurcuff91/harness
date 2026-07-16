package render

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/transport/tui/ansi"
)

// vgrid is a virtual terminal grid that emulates the subset of control
// sequences the renderer emits: text, \r, \n (with scroll at the bottom),
// cursor up/down (CSI A/B), clear-line (CSI 2K), and full clear
// (CSI 2J + H + 3J). It lets us assert the *visible result* of the
// differential renderer, catching scroll/duplication bugs that the
// write-capturing mock cannot.
type vgrid struct {
	cols, rows int
	grid       []string // visible rows (len == rows)
	scrollback []string // lines that scrolled off the top
	row, col   int      // cursor position (0-indexed, within visible grid)
}

func newVGrid(cols, rows int) *vgrid {
	g := &vgrid{cols: cols, rows: rows}
	g.grid = make([]string, rows)
	return g
}

// Terminal interface ---------------------------------------------------------

func (g *vgrid) Start(func(string), func()) error { return nil }
func (g *vgrid) Stop()                            {}
func (g *vgrid) Columns() int                     { return g.cols }
func (g *vgrid) Rows() int                        { return g.rows }
func (g *vgrid) MoveBy(int)                       {}
func (g *vgrid) HideCursor()                      {}
func (g *vgrid) ShowCursor()                      {}
func (g *vgrid) ClearLine()                       {}
func (g *vgrid) ClearFromCursor()                 {}
func (g *vgrid) ClearScreen()                     {}

var csiRe = regexp.MustCompile(`^\x1b\[([0-9;]*)([A-Za-z])`)

func (g *vgrid) Write(data string) {
	i := 0
	for i < len(data) {
		// Strip sync markers (no visual effect).
		if strings.HasPrefix(data[i:], ansi.SyncBegin) {
			i += len(ansi.SyncBegin)
			continue
		}
		if strings.HasPrefix(data[i:], ansi.SyncEnd) {
			i += len(ansi.SyncEnd)
			continue
		}
		// Full clear: 2J then H then 3J.
		if strings.HasPrefix(data[i:], "\x1b[2J") {
			for r := range g.grid {
				g.grid[r] = ""
			}
			g.row, g.col = 0, 0
			i += len("\x1b[2J")
			continue
		}
		if strings.HasPrefix(data[i:], "\x1b[H") {
			g.row, g.col = 0, 0
			i += len("\x1b[H")
			continue
		}
		if strings.HasPrefix(data[i:], "\x1b[3J") {
			g.scrollback = nil
			i += len("\x1b[3J")
			continue
		}
		// SGR / OSC / APC — consume, no grid effect (color only).
		if code, length := ansi.ExtractAnsiCode(data[i:], 0); length > 0 && strings.HasSuffix(code, "m") {
			i += length
			continue
		}
		// CSI cursor/clear sequences.
		if m := csiRe.FindStringSubmatch(data[i:]); m != nil {
			n := 1
			if m[1] != "" {
				n, _ = strconv.Atoi(strings.Split(m[1], ";")[0])
			}
			switch m[2] {
			case "A": // up
				g.row -= n
				if g.row < 0 {
					g.row = 0
				}
			case "B": // down — real terminals CLAMP at the bottom, no scroll
				g.row += n
				if g.row >= g.rows {
					g.row = g.rows - 1
				}
			case "K": // clear line (2K = whole line)
				g.grid[g.row] = ""
			}
			i += len(m[0])
			continue
		}
		// CR.
		if data[i] == '\r' {
			g.col = 0
			i++
			continue
		}
		// LF.
		if data[i] == '\n' {
			g.row++
			g.col = 0
			for g.row >= g.rows {
				g.scrollUp()
				g.row--
			}
			i++
			continue
		}
		// Printable rune.
		r, size := decodeNextRune(data[i:])
		g.putRune(r)
		i += size
	}
}

func (g *vgrid) scrollUp() {
	g.scrollback = append(g.scrollback, g.grid[0])
	copy(g.grid, g.grid[1:])
	g.grid[g.rows-1] = ""
}

func (g *vgrid) putRune(r rune) {
	if g.row < 0 || g.row >= g.rows {
		return
	}
	line := []rune(g.grid[g.row])
	// Pad with spaces up to col.
	for len(line) < g.col {
		line = append(line, ' ')
	}
	if g.col < len(line) {
		line[g.col] = r
	} else {
		line = append(line, r)
	}
	g.grid[g.row] = string(line)
	g.col++
}

func decodeNextRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

// allLines returns scrollback + visible grid, trimmed of trailing blank rows.
func (g *vgrid) allLines() []string {
	all := append([]string{}, g.scrollback...)
	all = append(all, g.grid...)
	// Trim trailing empties.
	for len(all) > 0 && strings.TrimSpace(all[len(all)-1]) == "" {
		all = all[:len(all)-1]
	}
	return all
}

// ── The actual bug reproduction ─────────────────────────────────────────────

// growingComponent simulates streaming: its single logical line grows, and the
// full history is re-rendered each frame (like the chat scrollback).
type growingComponent struct{ text string }

func (c *growingComponent) Render(width int) []string {
	return ansi.WrapTextWithAnsi(c.text, width)
}

func TestStreamingLineNoDuplication(t *testing.T) {
	// Narrow terminal so the growing line wraps; short height so we scroll.
	g := newVGrid(20, 6)
	tui := New(g)
	comp := &growingComponent{}
	tui.AddChild(comp)

	// Simulate a line growing token by token, past the width (wrap) and past
	// the height (scroll).
	full := "1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20"
	tokens := strings.Split(full, " ")
	build := ""
	for _, tok := range tokens {
		if build != "" {
			build += " "
		}
		build += tok
		comp.text = build
		tui.doRender()
	}

	// The final visible+scrollback content must equal the final wrapped text,
	// with NO duplicated/partial intermediate lines.
	want := ansi.WrapTextWithAnsi(full, 20)
	got := g.allLines()

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("streaming produced duplicated/incorrect lines.\n--- got ---\n%s\n--- want ---\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAppendingLinesNoDuplication(t *testing.T) {
	g := newVGrid(40, 5)
	tui := New(g)
	comp := &growingComponent{}
	tui.AddChild(comp)

	// Append many lines (more than the height) — classic chat growth.
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
		comp.text = strings.Join(lines, "\n")
		tui.doRender()
	}

	want := lines
	got := g.allLines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("appending produced wrong lines.\n--- got ---\n%s\n--- want ---\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStreamingAtBottomAfterScroll(t *testing.T) {
	// Real chat scenario: history already fills past the screen, then a new
	// line streams in at the bottom, growing and wrapping. The growing line
	// must NOT leave duplicated partial copies in scrollback.
	g := newVGrid(30, 6)
	tui := New(g)
	comp := &growingComponent{}
	tui.AddChild(comp)

	// Pre-fill with stable history (8 lines > height 6).
	var history []string
	for i := 1; i <= 8; i++ {
		history = append(history, "history line "+strconv.Itoa(i))
	}
	base := strings.Join(history, "\n")
	comp.text = base
	tui.doRender()

	// Now stream a growing line at the bottom (wraps at width 30).
	full := "RESP: alpha beta gamma delta epsilon zeta eta theta iota kappa"
	tokens := strings.Split(full, " ")
	build := ""
	for _, tok := range tokens {
		if build != "" {
			build += " "
		}
		build += tok
		comp.text = base + "\n" + build
		tui.doRender()
	}

	want := append([]string{}, history...)
	want = append(want, ansi.WrapTextWithAnsi(full, 30)...)
	got := g.allLines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("streaming-at-bottom duplicated lines.\n--- got (%d) ---\n%s\n--- want (%d) ---\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}

// fixedFooter renders N stable lines (mimics sep/editor/footer below history).
type fixedFooter struct{ lines []string }

func (f *fixedFooter) Render(width int) []string { return f.lines }

func TestStreamingWithFixedFooterBelow(t *testing.T) {
	// The REAL layout: growing history on top, stable footer rows below.
	// As history grows past the screen, the frame scrolls. The growing line
	// must be rewritten in place, not duplicated into scrollback.
	g := newVGrid(40, 8)
	tui := New(g)
	history := &growingComponent{}
	footer := &fixedFooter{lines: []string{
		"────────────",
		"input here",
		"────────────",
		"info line",
		"footer line",
	}}
	tui.AddChild(history)
	tui.AddChild(footer)

	// History: 3 stable lines, then one growing line that wraps.
	base := "msg 1\nmsg 2\nmsg 3"
	full := "RESP alpha beta gamma delta epsilon zeta eta theta iota"
	tokens := strings.Split(full, " ")
	build := ""
	for _, tok := range tokens {
		if build != "" {
			build += " "
		}
		build += tok
		history.text = base + "\n" + build
		tui.doRender()
	}

	// Expected final visible+scrollback: history lines + footer, no dup partials.
	want := []string{"msg 1", "msg 2", "msg 3"}
	want = append(want, ansi.WrapTextWithAnsi(full, 40)...)
	want = append(want, footer.lines...)
	got := g.allLines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("FIXED-FOOTER streaming duplicated lines.\n--- got (%d) ---\n%s\n\n--- want (%d) ---\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}

// spinnerFooter changes its first line each render (mimics the animated spinner
// sitting below the growing history).
type spinnerFooter struct {
	frame int
	tail  []string
}

func (s *spinnerFooter) Render(width int) []string {
	s.frame++
	out := []string{"spin " + strconv.Itoa(s.frame%10)}
	return append(out, s.tail...)
}

func TestStreamingWithAnimatedSpinnerBelow(t *testing.T) {
	// The REAL failure mode: history grows while an animated spinner below it
	// also changes every frame. Coordinate tracking must stay consistent.
	g := newVGrid(40, 10)
	tui := New(g)
	history := &growingComponent{}
	foot := &spinnerFooter{tail: []string{"────", "input", "footer"}}
	tui.AddChild(history)
	tui.AddChild(foot)

	base := "msg 1\nmsg 2"
	full := "RESP alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu"
	tokens := strings.Split(full, " ")
	build := ""
	for _, tok := range tokens {
		if build != "" {
			build += " "
		}
		build += tok
		history.text = base + "\n" + build
		tui.doRender() // history grew
		tui.doRender() // spinner ticks (separate render)
	}

	want := []string{"msg 1", "msg 2"}
	want = append(want, ansi.WrapTextWithAnsi(full, 40)...)
	got := g.allLines()
	// Drop the trailing footer rows from got for history comparison.
	gotHistory := got
	if len(gotHistory) >= 4 {
		gotHistory = gotHistory[:len(gotHistory)-4] // spin + 3 tail
	}
	if strings.Join(gotHistory, "\n") != strings.Join(want, "\n") {
		t.Errorf("ANIMATED-SPINNER streaming duplicated history.\n--- got history (%d) ---\n%s\n\n--- want (%d) ---\n%s",
			len(gotHistory), strings.Join(gotHistory, "\n"), len(want), strings.Join(want, "\n"))
	}
}

func TestGrowingWrappedLineRewrite(t *testing.T) {
	// A single growing line that crosses wrap boundaries (1 visual line → 2 → 3)
	// must REPLACE its prior visual lines, not append duplicates. This is the
	// exact pattern from the bug report: "4. Tartan Army..." growing.
	g := newVGrid(20, 10)
	tui := New(g)
	comp := &growingComponent{}
	tui.AddChild(comp)

	// Grow a line word by word so it wraps to more and more visual rows.
	prefix := "4. item "
	words := []string{"aaaa", "bbbb", "cccc", "dddd", "eeee", "ffff", "gggg", "hhhh"}
	build := prefix
	for _, w := range words {
		build += w + " "
		comp.text = strings.TrimRight(build, " ")
		tui.doRender()
	}

	want := ansi.WrapTextWithAnsi(strings.TrimRight(build, " "), 20)
	got := g.allLines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("GROWING-WRAPPED line duplicated.\n--- got (%d) ---\n%s\n\n--- want (%d) ---\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}
