package ansi

import (
	"strings"
	"testing"
)

func TestVisibleWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"ansi colored", "\x1b[31mHello\x1b[0m", 5},
		{"ansi mixed", FG(HexErr, "Hello") + " " + FG(HexPrimary, "World"), 11},
		{"tab", "a\tb", 5}, // 1 + 3 + 1
		{"cjk", "你好", 4},
		// uniseg follows the Unicode standard: a bare pictograph (U+1F5E1)
		// defaults to text presentation (width 1). With VS16 it becomes an
		// emoji (width 2). Our persona emits the VS16 forms.
		{"emoji vs16", "🗡️", 2},
		{"emoji crossed swords", "⚔️", 2},
		{"emoji dart", "🎯", 2},
		{"emoji zwj family", "👨\u200d👩\u200d👧", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VisibleWidth(tt.in); got != tt.want {
				t.Errorf("VisibleWidth(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncateToWidth(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		maxWidth  int
		wantWidth int // visible width of result
	}{
		{"fits", "hello", 10, 5},
		{"exact", "hello", 5, 5},
		{"truncate ascii", "hello world", 8, 8},
		{"truncate with ellipsis", "abcdefghij", 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateToWidth(tt.in, tt.maxWidth, "...", false)
			if w := VisibleWidth(got); w > tt.maxWidth {
				t.Errorf("TruncateToWidth(%q, %d) width = %d, exceeds max", tt.in, tt.maxWidth, w)
			}
		})
	}
}

func TestTruncatePreservesAnsi(t *testing.T) {
	in := "\x1b[31mhello world\x1b[0m"
	got := TruncateToWidth(in, 8, "...", false)
	if VisibleWidth(got) > 8 {
		t.Errorf("width %d exceeds 8", VisibleWidth(got))
	}
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("lost color code: %q", got)
	}
	// Must end with a reset so style doesn't bleed.
	if !strings.HasSuffix(got, Reset) {
		t.Errorf("missing trailing reset: %q", got)
	}
}

func TestTruncatePad(t *testing.T) {
	got := TruncateToWidth("hi", 6, "...", true)
	if VisibleWidth(got) != 6 {
		t.Errorf("padded width = %d, want 6", VisibleWidth(got))
	}
}

func TestWrapTextWithAnsi(t *testing.T) {
	lines := WrapTextWithAnsi("the quick brown fox jumps", 10)
	for i, l := range lines {
		if VisibleWidth(l) > 10 {
			t.Errorf("line %d %q width %d exceeds 10", i, l, VisibleWidth(l))
		}
	}
	if len(lines) < 2 {
		t.Errorf("expected wrapping into multiple lines, got %d", len(lines))
	}
}

// TestWrapLongWordDoesNotOrphanPrefix guards the fix where a short prefix (e.g.
// a tool icon) followed by a very long space-free word was orphaned on its own
// line. The long word must continue filling the first line instead.
func TestWrapLongWordDoesNotOrphanPrefix(t *testing.T) {
	// "AB " (3 cols) + a 40-col word, wrapped at 20.
	long := strings.Repeat("x", 40)
	lines := WrapTextWithAnsi("AB "+long, 20)
	if len(lines) == 0 {
		t.Fatal("no lines")
	}
	// The first line must contain more than just the prefix — the long word
	// starts filling it.
	if VisibleWidth(lines[0]) <= 3 {
		t.Errorf("prefix orphaned: first line %q width %d", lines[0], VisibleWidth(lines[0]))
	}
	if !strings.HasPrefix(lines[0], "AB x") {
		t.Errorf("first line should be prefix+word start, got %q", lines[0])
	}
	// No line may exceed the width.
	for i, l := range lines {
		if VisibleWidth(l) > 20 {
			t.Errorf("line %d %q exceeds 20", i, l)
		}
	}
}

func TestWrapPreservesNewlines(t *testing.T) {
	lines := WrapTextWithAnsi("line one\nline two", 80)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "line one" || lines[1] != "line two" {
		t.Errorf("unexpected lines: %v", lines)
	}
}

func TestWrapCarriesAnsiAcrossLines(t *testing.T) {
	// A color opened on line 1 with a newline should be re-applied on line 2.
	in := "\x1b[31mred line one\nred line two\x1b[0m"
	lines := WrapTextWithAnsi(in, 80)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[1], "\x1b[") {
		t.Errorf("second line lost active style: %q", lines[1])
	}
}

func TestExtractAnsiCode(t *testing.T) {
	csi := "\x1b[31m"
	code, length := ExtractAnsiCode(csi+"x", 0)
	if code != csi || length != len(csi) {
		t.Errorf("CSI extract = %q,%d want %q,%d", code, length, csi, len(csi))
	}
	// No escape at position.
	if _, l := ExtractAnsiCode("abc", 0); l != 0 {
		t.Errorf("expected no extract, got length %d", l)
	}
}

func TestLongWordBreaks(t *testing.T) {
	lines := WrapTextWithAnsi("supercalifragilisticexpialidocious", 10)
	for i, l := range lines {
		if VisibleWidth(l) > 10 {
			t.Errorf("line %d width %d exceeds 10", i, VisibleWidth(l))
		}
	}
}

func TestColorHelpers(t *testing.T) {
	s := Primary("test")
	if !strings.HasPrefix(s, "\x1b[38;2;") {
		t.Errorf("Primary should emit truecolor, got %q", s)
	}
	if !strings.HasSuffix(s, Reset) {
		t.Errorf("Primary should auto-reset, got %q", s)
	}
	if VisibleWidth(s) != 4 {
		t.Errorf("colored width = %d, want 4", VisibleWidth(s))
	}
}

func TestEmojiPictographWidthOverride(t *testing.T) {
	// Text-presentation pictographs (no VS16) must report width 2, matching how
	// terminals render them, so following text is not "eaten".
	cases := []struct {
		name string
		in   string
	}{
		{"plane no vs16", "\u2708"},        // ✈
		{"thermometer no vs16", "\U0001F321"}, // 🌡
		{"tornado", "\U0001F300"},          // 🌀
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := VisibleWidth(c.in); w != 2 {
				t.Errorf("VisibleWidth(%q) = %d, want 2", c.in, w)
			}
		})
	}
}
