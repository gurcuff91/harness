package telegram

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTableToCodeBlock(t *testing.T) {
	in := "| Campo | Valor |\n|-------|-------|\n| Slug | abc |\n| Runs | 7 |"
	got := tablesToCodeBlocks(in)

	// Wrapped in a fence.
	if !strings.HasPrefix(got, "```") || !strings.HasSuffix(got, "```") {
		t.Fatalf("table should be fenced:\n%s", got)
	}
	// Structure is preserved: pipe borders remain.
	if !strings.Contains(got, "|") {
		t.Errorf("table borders should be preserved:\n%s", got)
	}
	// Header and cells present.
	for _, want := range []string{"Campo", "Valor", "Slug", "abc", "Runs"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	assertAligned(t, got)
}

// assertAligned checks every bordered row line has the same VISUAL width
// (rune count, not bytes) and that the pipe positions line up — the real test
// of column alignment in a monospace font.
func assertAligned(t *testing.T, rendered string) {
	t.Helper()
	width, pipes := -1, ""
	for _, line := range strings.Split(rendered, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		w := utf8.RuneCountInString(line)
		// Positions (by rune index) of each pipe in the line.
		var pos strings.Builder
		for i, r := range []rune(line) {
			if r == '|' {
				fmt.Fprintf(&pos, "%d,", i)
			}
		}
		if width == -1 {
			width, pipes = w, pos.String()
			continue
		}
		if w != width {
			t.Errorf("row visual width %d != %d:\n%s", w, width, rendered)
		}
		if pos.String() != pipes {
			t.Errorf("pipe columns misaligned (%s vs %s):\n%s", pos.String(), pipes, rendered)
		}
	}
}

// The alignment must hold when cells contain multi-byte runes (accents), which
// is where a byte-based width would break.
func TestTableAlignmentWithAccents(t *testing.T) {
	in := "| Producto | Categoría | Precio |\n|---|---|---|\n" +
		"| Auriculares X200 | Electrónica | $59.99 |\n" +
		"| Cafetera Aroma | Hogar | $89.50 |\n" +
		"| Lámpara LED Flex | Iluminación | $32.20 |"
	assertAligned(t, tablesToCodeBlocks(in))
}

func TestNonTableUntouched(t *testing.T) {
	in := "Just text.\n- a list item\n- another"
	if got := tablesToCodeBlocks(in); got != in {
		t.Errorf("non-table text should be unchanged:\n%s", got)
	}
}

// A pipe used in prose (not a real table — no delimiter row) is left alone.
func TestPipeWithoutDelimiter(t *testing.T) {
	in := "use a | b to pipe"
	if got := tablesToCodeBlocks(in); got != in {
		t.Errorf("a stray pipe should not trigger table handling:\n%s", got)
	}
}

// Tables inside an existing code fence are not re-processed.
func TestTableInsideFenceUntouched(t *testing.T) {
	in := "```\n| a | b |\n|---|---|\n| 1 | 2 |\n```"
	if got := tablesToCodeBlocks(in); got != in {
		t.Errorf("table inside a fence should be left alone:\n%s", got)
	}
}

// End-to-end through the MarkdownV2 converter: the table becomes a fence and
// the period after it is still escaped (normal text handling continues).
func TestTableThroughConverter(t *testing.T) {
	in := "Data:\n| k | v |\n|---|---|\n| x | y |\nDone."
	got := toMarkdownV2(in)
	if !strings.Contains(got, "```") {
		t.Errorf("expected a code fence:\n%s", got)
	}
	if !strings.Contains(got, "Done\\.") {
		t.Errorf("text after the table should still be escaped:\n%s", got)
	}
}
