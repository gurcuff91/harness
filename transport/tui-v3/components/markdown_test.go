package components

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
)

// feedAll runs text through the streaming renderer one rune at a time (worst
// case for state tracking) and returns the full output + flush.
func feedAll(text string) string {
	m := NewMarkdownStream()
	var out strings.Builder
	for _, ch := range text {
		out.WriteString(m.Feed(string(ch)))
	}
	out.WriteString(m.Flush())
	return out.String()
}

func TestMarkdownBold(t *testing.T) {
	out := feedAll("hello **world**")
	if !strings.Contains(out, ansi.Bold) {
		t.Errorf("bold not applied: %q", out)
	}
	if !strings.Contains(out, "world") {
		t.Errorf("content missing: %q", out)
	}
}

func TestMarkdownItalic(t *testing.T) {
	out := feedAll("an *emphasis* word")
	if !strings.Contains(out, ansi.Ital) {
		t.Errorf("italic not applied: %q", out)
	}
}

func TestMarkdownHeading(t *testing.T) {
	out := feedAll("# Title\n")
	if !strings.Contains(out, accentFG) {
		t.Errorf("heading accent not applied: %q", out)
	}
	if !strings.Contains(out, "Title") {
		t.Errorf("heading text missing: %q", out)
	}
}

func TestMarkdownBullet(t *testing.T) {
	out := feedAll("- item one\n")
	if !strings.Contains(out, "•") {
		t.Errorf("bullet not rendered: %q", out)
	}
}

func TestMarkdownInlineCode(t *testing.T) {
	out := feedAll("use `go build` now")
	if !strings.Contains(out, "go build") {
		t.Errorf("inline code content missing: %q", out)
	}
	if !strings.Contains(out, accentFG) {
		t.Errorf("inline code styling missing: %q", out)
	}
}

func TestMarkdownCodeBlock(t *testing.T) {
	out := feedAll("```go\nfmt.Println()\n```")
	if !strings.Contains(out, "fmt.Println()") {
		t.Errorf("code block content missing: %q", out)
	}
}

func TestMarkdownPlainText(t *testing.T) {
	out := feedAll("just plain text")
	if !strings.Contains(out, "just plain text") {
		t.Errorf("plain text mangled: %q", out)
	}
}

func TestMarkdownChunkedVsCharByChar(t *testing.T) {
	// Feeding in arbitrary chunks must produce the same result as char-by-char.
	text := "# Head\n\nSome **bold** and *italic* and `code`.\n\n- a\n- b\n"
	charByChar := feedAll(text)

	m := NewMarkdownStream()
	var chunked strings.Builder
	chunks := []string{"# Head\n\nSo", "me **bo", "ld** and *ita", "lic* and `co", "de`.\n\n- a\n- b\n"}
	for _, c := range chunks {
		chunked.WriteString(m.Feed(c))
	}
	chunked.WriteString(m.Flush())

	if charByChar != chunked.String() {
		t.Errorf("streaming inconsistent:\nchar: %q\nchunk: %q", charByChar, chunked.String())
	}
}

func TestMarkdownTable(t *testing.T) {
	out := feedAll("| A | B |\n|---|---|\n| 1 | 2 |\n")
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Errorf("table headers missing: %q", out)
	}
	if !strings.Contains(out, "│") {
		t.Errorf("table column separator missing: %q", out)
	}
}

func TestMarkdownBoldInlineCodeCombo(t *testing.T) {
	// "**`code`**" must NOT leak literal ** markers (bug: emphasis pending when
	// an inline-code span opens).
	out := feedAll("**`AGENTS.md`**: text")
	if strings.Contains(out, "**") {
		t.Errorf("literal ** leaked: %q", out)
	}
	if !strings.Contains(out, "AGENTS.md") {
		t.Errorf("code content lost: %q", out)
	}
}

func TestMarkdownNumberedListWithBold(t *testing.T) {
	// "1. **item**" must keep the number prefix in order (bug: "1." moved to end).
	out := feedAll("1. **item one**")
	stripped := stripANSIForTest(out)
	if !strings.HasPrefix(stripped, "1. ") {
		t.Errorf("numbered prefix misplaced: %q", stripped)
	}
	if strings.Contains(out, "**") {
		t.Errorf("literal ** leaked: %q", out)
	}
}

func TestMarkdownNumberedListMultiline(t *testing.T) {
	out := feedAll("1. uno\n2. dos\n3. tres")
	stripped := stripANSIForTest(out)
	for _, want := range []string{"1. uno", "2. dos", "3. tres"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("numbered item %q missing in %q", want, stripped)
		}
	}
}

func TestMarkdownTableFaithfulSpacing(t *testing.T) {
	// The renderer reproduces EXACTLY the newlines the model sent after a table
	// — no forced blank lines. The table ends with the bottom border "┘".
	noBlank := stripANSIForTest(feedAll("| A |\n|---|\n| 1 |\ntext"))
	if strings.Contains(noBlank, "┘\n\ntext") {
		t.Errorf("renderer injected a blank line the model did not send: %q", noBlank)
	}
	if !strings.Contains(noBlank, "┘\ntext") {
		t.Errorf("expected table bottom border directly followed by text: %q", noBlank)
	}
	// With one model blank line, exactly one blank line is preserved.
	withBlank := stripANSIForTest(feedAll("| A |\n|---|\n| 1 |\n\ntext"))
	if !strings.Contains(withBlank, "┘\n\ntext") {
		t.Errorf("model's single blank line not preserved after table: %q", withBlank)
	}
}

// stripANSIForTest removes SGR codes for assertion readability.
func stripANSIForTest(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestMarkdownHeadingThenTableSpacing(t *testing.T) {
	// "## H\n\n| table": the heading is followed by exactly the one blank line
	// the model sent (2 newlines) before the table's top border — the renderer
	// must not add or drop newlines.
	out := stripANSIForTest(feedAll("## Stack\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\nnext"))
	hi := strings.Index(out, "Stack")
	ti := strings.Index(out, "┌") // table top border
	if hi < 0 || ti < 0 {
		t.Fatalf("heading/table not rendered: %q", out)
	}
	between := out[hi+len("Stack") : ti]
	if n := strings.Count(between, "\n"); n != 2 {
		t.Errorf("heading→table should preserve the model's 2 newlines, got %d: %q", n, between)
	}
}

func TestMarkdownPreservesModelBlankLines(t *testing.T) {
	// The renderer is faithful: the number of newlines out equals in. Models
	// own the spacing; the renderer only styles, never reflows blank lines.
	for _, in := range []string{"A\n\nB", "A\n\n\nB", "A\n\n\n\nB"} {
		out := stripANSIForTest(feedAll(in))
		if out != in {
			t.Errorf("blank lines not preserved: in %q -> out %q", in, out)
		}
	}
}

func TestMarkdownTableFitsWidth(t *testing.T) {
	// A wide table must wrap its cells so no rendered line exceeds the width.
	m := NewMarkdownStream()
	m.SetWidth(60)
	in := "| Col A | Col B |\n|---|---|\n| short | this is a very long cell value that must wrap to fit |"
	out := m.Feed(in) + m.Flush()
	for _, line := range strings.Split(out, "\n") {
		// strip ANSI for width check
		clean := stripANSIForTest(line)
		if w := visibleW(clean); w > 60 {
			t.Errorf("table line exceeds width 60 (%d): %q", w, clean)
		}
	}
	// Must have box-drawing borders.
	if !strings.Contains(out, "┌") || !strings.Contains(out, "┘") {
		t.Errorf("table missing box borders: %q", out)
	}
}

// visibleW counts runes ignoring nothing (input already ANSI-stripped); good
// enough for ASCII table assertions.
func visibleW(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func TestMarkdownLink(t *testing.T) {
	// Link with differing text/url shows "text (url)".
	out := stripANSIForTest(feedAll("see [Google](https://google.com) now"))
	if !strings.Contains(out, "Google") || !strings.Contains(out, "(https://google.com)") {
		t.Errorf("link not rendered: %q", out)
	}
	if strings.Contains(out, "[Google]") {
		t.Errorf("raw link markup leaked: %q", out)
	}
	// Link where text == url shows just once.
	same := stripANSIForTest(feedAll("[https://x.com](https://x.com)"))
	if strings.Count(same, "https://x.com") != 1 {
		t.Errorf("equal text/url should show once: %q", same)
	}
}

func TestMarkdownLinkNotALink(t *testing.T) {
	// "[text]" without "(url)" stays literal.
	out := stripANSIForTest(feedAll("a [bracket] here"))
	if !strings.Contains(out, "[bracket]") {
		t.Errorf("non-link brackets should stay literal: %q", out)
	}
}

func TestMarkdownStrikethrough(t *testing.T) {
	out := feedAll("this is ~~gone~~ text")
	if !strings.Contains(out, ansi.Strike) {
		t.Errorf("strikethrough not applied: %q", out)
	}
	clean := stripANSIForTest(out)
	if strings.Contains(clean, "~~") {
		t.Errorf("raw ~~ leaked: %q", clean)
	}
}

func TestMarkdownHeadingLevels(t *testing.T) {
	h1 := feedAll("# Title\n")
	if !strings.Contains(h1, ansi.Bold) || !strings.Contains(h1, ansi.Under) {
		t.Errorf("H1 should be bold+underline: %q", h1)
	}
	h2 := feedAll("## Sub\n")
	if !strings.Contains(h2, ansi.Bold) {
		t.Errorf("H2 should be bold: %q", h2)
	}
	h3 := stripANSIForTest(feedAll("### Deep\n"))
	if !strings.Contains(h3, "### Deep") {
		t.Errorf("H3 should show the ### prefix: %q", h3)
	}
}

func TestMarkdownHR(t *testing.T) {
	m := NewMarkdownStream()
	m.SetWidth(120)
	out := stripANSIForTest(m.Feed("---\n") + m.Flush())
	// Decorative rule: present, but capped at 30 columns (not full width).
	if !strings.Contains(out, strings.Repeat("─", 30)) {
		t.Errorf("HR should be a 30-col rule: %q", out)
	}
	if strings.Contains(out, strings.Repeat("─", 31)) {
		t.Errorf("HR should not exceed 30 cols: %q", out)
	}
}

func TestMarkdownLinkOSC8(t *testing.T) {
	// Links emit an OSC 8 hyperlink escape so terminals can make them clickable.
	out := feedAll("[Google](https://google.com)")
	if !strings.Contains(out, "\x1b]8;;https://google.com\x1b\\") {
		t.Errorf("OSC 8 hyperlink open sequence missing: %q", out)
	}
	if !strings.Contains(out, "\x1b]8;;\x1b\\") {
		t.Errorf("OSC 8 hyperlink close sequence missing: %q", out)
	}
	// The URL inside the escape must not count toward visible width.
	if w := ansi.VisibleWidth(out); w > 40 {
		t.Errorf("hyperlink URL leaked into visible width (%d): %q", w, out)
	}
}

func TestMarkdownTaskList(t *testing.T) {
	unchecked := stripANSIForTest(feedAll("- [ ] pending\n"))
	if !strings.Contains(unchecked, "☐") {
		t.Errorf("unchecked task box missing: %q", unchecked)
	}
	checked := stripANSIForTest(feedAll("- [x] done\n"))
	if !strings.Contains(checked, "☑") {
		t.Errorf("checked task box missing: %q", checked)
	}
}

func TestMarkdownPlainBulletStillWorks(t *testing.T) {
	out := stripANSIForTest(feedAll("- item one\n"))
	if !strings.Contains(out, "• item one") {
		t.Errorf("plain bullet broke: %q", out)
	}
}

func TestMarkdownBlockReRendersOnResize(t *testing.T) {
	// A source-backed Markdown block must re-lay-out its table to a new width
	// after Invalidate() — the core of resize correctness.
	b := NewMarkdown("| A | B |\n|---|---|\n| x | a fairly long value that definitely needs to wrap when the terminal is narrow enough to force it |")

	wide := b.Render(120)
	for _, l := range wide {
		if ansi.VisibleWidth(l) > 120 {
			t.Errorf("wide render exceeds 120: %q", l)
		}
	}

	b.Invalidate()
	narrow := b.Render(40)
	for _, l := range narrow {
		if ansi.VisibleWidth(l) > 40 {
			t.Errorf("narrow render exceeds 40 (resize broken): %q (w=%d)", l, ansi.VisibleWidth(l))
		}
	}
	// The narrow render must differ from the wide one (it actually re-laid-out).
	if strings.Join(wide, "\n") == strings.Join(narrow, "\n") {
		t.Errorf("block did not re-layout on resize")
	}
}

// TestTableFlushesBeforeFollowingBlock guards against a regression where a
// buffered table was emitted AFTER the text that followed it (and its top
// border pasted onto the previous line). Every block type that can open a new
// line right after a table must flush the table first.
func TestTableFlushesBeforeFollowingBlock(t *testing.T) {
	cases := map[string]string{
		"bold":     "| A | B |\n|---|---|\n| x | y |\n\n**Despliegue** texto\n",
		"text":     "| A | B |\n|---|---|\n| x | y |\n\nTexto plano\n",
		"link":     "| A | B |\n|---|---|\n| x | y |\n\n[link](http://e.com)\n",
		"backtick": "| A | B |\n|---|---|\n| x | y |\n\n`code` texto\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			lines := stripLines(NewMarkdown(src).Render(100))
			topBorder, follow := -1, -1
			for i, l := range lines {
				if strings.HasPrefix(l, "\u250c") { // ┌ top-left corner
					topBorder = i
				}
				if follow == -1 && (strings.Contains(l, "Despliegue") ||
					strings.Contains(l, "Texto") || strings.Contains(l, "link") ||
					strings.Contains(l, "code")) {
					follow = i
				}
			}
			if topBorder == -1 {
				t.Fatalf("no table top border rendered: %q", lines)
			}
			// Border must start at column 0 (its own line), not pasted onto text.
			if ansi.VisibleWidth(lines[topBorder]) == 0 || !strings.HasPrefix(lines[topBorder], "\u250c") {
				t.Errorf("table border not on its own line: %q", lines[topBorder])
			}
			// The following block's text must appear AFTER the table.
			if follow != -1 && follow < topBorder {
				t.Errorf("following text (line %d) rendered before table (line %d): %q", follow, topBorder, lines)
			}
		})
	}
}

// stripLines removes SGR escape sequences from each line for assertion.
func stripLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		var b strings.Builder
		j := 0
		for j < len(l) {
			if l[j] == 0x1b {
				for j < len(l) && l[j] != 'm' {
					j++
				}
				j++
				continue
			}
			b.WriteByte(l[j])
			j++
		}
		out[i] = b.String()
	}
	return out
}

func TestRawBlockReWrapsOnResize(t *testing.T) {
	b := NewRawBlock("a line of plain text that is long enough to wrap when narrow")
	wide := b.Render(80)
	b.Invalidate()
	narrow := b.Render(20)
	for _, l := range narrow {
		if ansi.VisibleWidth(l) > 20 {
			t.Errorf("raw block exceeds 20 after resize: %q", l)
		}
	}
	if len(narrow) <= len(wide) {
		t.Errorf("narrow raw block should wrap into more lines: wide=%d narrow=%d", len(wide), len(narrow))
	}
}
