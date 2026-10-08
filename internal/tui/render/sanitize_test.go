package render

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// The cached sanitizer must produce exactly what the stateless one does, frame
// after frame: unchanged lines reused, changed lines re-measured, over-wide
// lines clipped whether they're new or carried over.
func TestLineSanitizerMatchesStateless(t *testing.T) {
	const width = 10
	wide := "this line is way too long for ten cols"
	frames := [][]string{
		{"short", wide, "ok"},
		{"short", wide, "ok", "appended"},              // append
		{"short", "now short", "ok", "appended"},       // mid-buffer change
		{"short", "now short"},                         // shrink
		{"short", "now short", wide + " (new)", "x"},   // grow with a wide line
		{"changed", "now short", wide + " (new)", "x"}, // first line changes
	}
	var s lineSanitizer
	for n, f := range frames {
		want := sanitizeLines(append([]string(nil), f...), width)
		got := s.sanitize(append([]string(nil), f...), width)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("frame %d: cached = %q, stateless = %q", n, got, want)
		}
		for _, l := range got {
			if ansi.VisibleWidth(l) > width {
				t.Fatalf("frame %d: line %q exceeds width %d", n, l, width)
			}
		}
	}
}

// A width change must invalidate the cache: a line that fit before may not
// fit now (and vice versa).
func TestLineSanitizerRemeasuresOnWidthChange(t *testing.T) {
	var s lineSanitizer
	line := "exactly-twenty-chars"
	if got := s.sanitize([]string{line}, 40); got[0] != line {
		t.Fatalf("at width 40: %q, want unclipped", got[0])
	}
	got := s.sanitize([]string{line}, 8)
	if ansi.VisibleWidth(got[0]) > 8 {
		t.Fatalf("at width 8: %q not clipped (stale cache?)", got[0])
	}
	if got := s.sanitize([]string{line}, 40); got[0] != line {
		t.Fatalf("back at width 40: %q, want unclipped again", got[0])
	}
}

// Over-wide content stays clipped across frames through the real render path
// (reused from the cache on the second frame).
func TestSanitizeOverWideLinesAcrossFrames(t *testing.T) {
	tui, term := newTestTUI(10, 24)
	comp := &staticComponent{lines: []string{"this line is way too long for ten cols"}}
	tui.AddChild(comp)
	tui.doRender()
	comp.lines = append(comp.lines, "and another one that is far too wide")
	tui.doRender()
	for _, l := range strings.Split(term.lastWrite(), "\n") {
		l = strings.TrimPrefix(strings.TrimSuffix(l, "\x1b[?2026l"), "\x1b[?2026h")
		if vw := visibleLen(l); vw > 10 {
			t.Errorf("over-wide line not clipped on a later frame: width %d in %q", vw, l)
		}
	}
}
