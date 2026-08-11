package components

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/tui/ansi"
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

// TestInlineCodeOrder guards against a regression where line-start text buffered
// in linePrefix leaked out AFTER an inline code span, reordering the output
// (e.g. "`agi` y `cm`" rendered as "agicm y").
func TestInlineCodeOrder(t *testing.T) {
	stripped := func(s string) string {
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
	cases := map[string]string{
		"`agi` y `cm` son alias": "agi y cm son alias",
		"`code` texto":           "code texto",
		"`a` `b` `c`":            "a b c",
	}
	for src, want := range cases {
		if got := stripped(feedAll(src)); got != want {
			t.Errorf("feedAll(%q) = %q, want %q", src, got, want)
		}
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

// TestMarkdownTableEmojiRightPadding verifies that every cell has at least 1
// column of right-side padding between the cell text and the `│` border. Some
// terminals render emoji ZWJ / VS-16 sequences wider than uniseg reports (e.g.
// 👨‍💻 can render at 4 columns even though we compute 2). Without this slack the
// wider glyph overwrites the border and breaks the column alignment. The fix
// reserves 1 column per cell as safety padding; this test asserts it survives.
func TestMarkdownTableEmojiRightPadding(t *testing.T) {
	cases := []struct {
		name string
		md   string
	}{
		{"zwj_man_computer", "| Name | Status |\n|------|--------|\n| 👨‍💻 dev | active |\n| 👩‍🔬 sci | active |\n"},
		{"zwj_rainbow_flag", "| Flag | State |\n|------|-------|\n| 🏳️‍🌈 pride | on |\n"},
		{"regional_flag", "| Country | Code |\n|---------|------|\n| 🇺🇸 USA | US |\n| 🇯🇵 Japan | JP |\n"},
		{"vs16_check", "| State | Done |\n|-------|------|\n| ☑️ yes | ✔️ |\n| ☐ no | ✖️ |\n"},
		{"plain_text_safety", "| A | B |\n|---|---|\n| x | y |\n"},
		{"single_emoji", "| E | S |\n|---|---|\n| ✅ | ☐ |\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := feedAll(c.md)
			stripped := stripANSIForTest(out)
			// Every body row should end with ` │` — exactly 1 space between the
			// last cell character and the right border.
			for _, line := range strings.Split(stripped, "\n") {
				if !strings.Contains(line, "│") {
					continue
				}
				if strings.HasPrefix(line, "┌") || strings.HasPrefix(line, "├") || strings.HasPrefix(line, "└") {
					continue
				}
				// Last char of a body row must be the right border pipe.
				if !strings.HasSuffix(line, "│") {
					t.Errorf("%s: row does not end with right border: %q", c.name, line)
				}
				// Char before the final pipe must be a space (the reserved
				// padding). If it's something else, an emoji overran the budget.
				trimmed := strings.TrimSuffix(line, "│")
				if len(trimmed) == 0 {
					t.Errorf("%s: row has no content before right border: %q", c.name, line)
					continue
				}
				last := trimmed[len(trimmed)-1]
				if last != ' ' {
					t.Errorf("%s: cell text touches right border (no safety padding) on row %q (last char=%q)", c.name, line, last)
				}
			}
		})
	}
}

// TestMarkdownTableEmojiAlignmentZwj specifically guards against the original
// regression where 👨‍💻 / 🏳️‍🌈 / 🇺🇸 (ZWJ + regional-indicator clusters) caused
// the row borders to misalign because uniseg reports them as 2 columns while
// some terminals render them wider. We assert all body rows have the same
// visible width as the header row.
func TestMarkdownTableEmojiAlignmentZwj(t *testing.T) {
	md := "| Name | Status |\n|------|--------|\n| 👨‍💻 dev | active |\n| 👩‍🔬 sci | active |\n| 🏳️‍🌈 flag | test |\n| 🇺🇸 US | test |\n"
	out := feedAll(md)
	stripped := stripANSIForTest(out)

	var widths []int
	for _, line := range strings.Split(stripped, "\n") {
		if !strings.Contains(line, "│") {
			continue
		}
		if strings.HasPrefix(line, "┌") || strings.HasPrefix(line, "├") || strings.HasPrefix(line, "└") {
			continue
		}
		w := ansi.VisibleWidth(line)
		widths = append(widths, w)
		t.Logf("row width=%d bytes=%d: %q", w, len(line), line)
	}
	if len(widths) < 2 {
		t.Fatalf("expected at least 2 body rows, got %d", len(widths))
	}
	first := widths[0]
	for i, w := range widths {
		if w != first {
			t.Errorf("row %d width=%d, expected %d (rows misaligned with ZWJ/regional-indicator emoji)", i, w, first)
		}
	}
}

// TestMarkdownTableEmojiMixedWidths verifies that the safety padding keeps a
// table aligned even when cells mix plain ASCII, single-codepoint emoji, and
// ZWJ/VS-16 clusters in the same column.
func TestMarkdownTableEmojiMixedWidths(t *testing.T) {
	md := "| Mark | State |\n|------|-------|\n| ok | done |\n| ✅ | yes |\n| 👨‍💻 | coding |\n| 🏳️‍🌈 | pride |\n"
	out := feedAll(md)
	stripped := stripANSIForTest(out)

	var bodyWidths []int
	for _, line := range strings.Split(stripped, "\n") {
		if strings.HasPrefix(line, "│") && strings.Contains(line, "│") {
			bodyWidths = append(bodyWidths, ansi.VisibleWidth(line))
		}
	}
	if len(bodyWidths) < 2 {
		t.Fatalf("expected multiple body rows, got %d", len(bodyWidths))
	}
	for i := 1; i < len(bodyWidths); i++ {
		if bodyWidths[i] != bodyWidths[0] {
			t.Errorf("row %d width=%d differs from row 0 width=%d (rows: %v)", i, bodyWidths[i], bodyWidths[0], bodyWidths)
		}
	}
}

// TestSplitTablePipesIgnoresPipesInCodeSpan is the regression test for phantom
// columns: a table cell containing inline code with pipes — e.g.
// `off|low|medium|high|xhigh` — must stay ONE cell, not split into five. A
// plain strings.Split(line, "|") (what this replaced) was blind to backticks
// and produced unheadered phantom columns in the rendered table.
func TestSplitTablePipesIgnoresPipesInCodeSpan(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "pipes inside inline code",
			line: "| level | `off|low|medium|high|xhigh` |",
			want: []string{"", " level ", " `off|low|medium|high|xhigh` ", ""},
		},
		{
			name: "plain row unaffected",
			line: "| a | b | c |",
			want: []string{"", " a ", " b ", " c ", ""},
		},
		{
			name: "escaped pipe is literal",
			line: "| a\\|b | c |",
			want: []string{"", " a|b ", " c ", ""},
		},
		{
			name: "code span reopens after closing",
			line: "| `x|y` | `z|w` |",
			want: []string{"", " `x|y` ", " `z|w` ", ""},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitTablePipes(c.line)
			if len(got) != len(c.want) {
				t.Fatalf("got %d parts %q, want %d %q", len(got), got, len(c.want), c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("part[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestSplitTableCellsCodeSpanColumnCount verifies the column COUNT a real
// table row yields: the reported bug had a "Validación" cell of
// `off|low|medium|high|xhigh` inflate a 5-column table into 9 columns.
func TestSplitTableCellsCodeSpanColumnCount(t *testing.T) {
	header := splitTableCells("| Grupo | Lectura | Escritura | Validación | Extra |")
	row := splitTableCells("| ThinkingLevel | Thinking() | SetThinking() | `off|low|medium|high|xhigh` | ok |")
	if len(header) != 5 {
		t.Fatalf("header cell count = %d, want 5", len(header))
	}
	if len(row) != len(header) {
		t.Errorf("row cell count = %d, want %d (matching header) — code-span pipes leaked as columns", len(row), len(header))
	}
	// The code-span cell must survive intact.
	if row[3] != "`off|low|medium|high|xhigh`" {
		t.Errorf("code-span cell mangled: %q", row[3])
	}
}

// TestMarkdownTableWithCodeSpanPipesRendersOneColumn is the end-to-end guard:
// the streamed renderer must not emit phantom columns for the pipes inside an
// inline-code cell. A correct 2-column table has exactly the header cells; the
// bug produced extra separators for low/medium/high/xhigh.
func TestMarkdownTableWithCodeSpanPipesRendersOneColumn(t *testing.T) {
	table := "| level | values |\n|---|---|\n| thinking | `off|low|medium|high|xhigh` |\n"
	out := feedAll(table)
	stripped := stripANSIForTest(out)

	// Every content line must have the SAME number of column separators (│).
	var counts []int
	for _, ln := range strings.Split(stripped, "\n") {
		if strings.Contains(ln, "│") {
			counts = append(counts, strings.Count(ln, "│"))
		}
	}
	if len(counts) == 0 {
		t.Fatalf("no table rows rendered: %q", stripped)
	}
	for i, c := range counts {
		if c != counts[0] {
			t.Errorf("row %d has %d separators, first row has %d — ragged columns from code-span pipes:\n%s",
				i, c, counts[0], stripped)
		}
	}
	// The values must remain together on one line (not split across cells).
	if !strings.Contains(stripped, "off|low|medium|high|xhigh") {
		t.Errorf("inline-code value was split apart: %q", stripped)
	}
}

// codeGreen is the ANSI prefix code-block/inline-code content is styled with
// (accent fg + italic). Used to assert whether text was rendered AS code.
const codeGreen = "\x1b[38;2;200;217;106m\x1b[3m"

// TestCodeFenceFourBackticks is the regression test for the reported bug: a
// code fence opened with 4 backticks (````) desynced the parser — it opened on
// the first 3 and leaked the 4th, so the closing fence looked shorter than the
// opening one and the following markdown was mis-rendered. Per CommonMark a
// fence is 3 OR MORE backticks and the close must be at least as long as the
// open.
func TestCodeFenceFourBackticks(t *testing.T) {
	md := "````go\ncode line\n````\ntexto `inline` despues\n"
	out := feedAll(md)
	stripped := stripANSIForTest(out)

	// The opening and closing fences must have the SAME backtick count (4).
	if !strings.Contains(stripped, "````go") {
		t.Errorf("opening fence lost its 4 backticks: %q", stripped)
	}
	// Count "````" occurrences (open + close) — must be exactly 2 fences of 4.
	if n := strings.Count(stripped, "````"); n != 2 {
		t.Errorf("expected 2 four-backtick fences (open+close), got %d:\n%s", n, stripped)
	}
	// The text after the block must NOT be styled as code (parser not trapped).
	if strings.Contains(out, codeGreen+"texto") {
		t.Errorf("text after a 4-backtick block was trapped as code:\n%s", stripped)
	}
}

// TestCodeFenceFiveBackticks verifies the general N>=3 rule with a 5-backtick
// fence.
func TestCodeFenceFiveBackticks(t *testing.T) {
	out := feedAll("`````\ncode\n`````\nafter\n")
	stripped := stripANSIForTest(out)
	if n := strings.Count(stripped, "`````"); n != 2 {
		t.Errorf("expected 2 five-backtick fences, got %d:\n%s", n, stripped)
	}
	if strings.Contains(out, codeGreen+"after") {
		t.Errorf("text after a 5-backtick block was trapped as code:\n%s", stripped)
	}
}

// TestCodeFenceLongerFenceHoldsShorterBackticksAsContent is the whole POINT of
// longer fences: a ```` block can contain a ``` line as literal content
// without closing early (that's why models emit 4 backticks — the code inside
// has 3). The inner ``` must stay part of the code, and the block must close
// only on the matching 4-backtick fence.
func TestCodeFenceLongerFenceHoldsShorterBackticksAsContent(t *testing.T) {
	md := "````\nx := \"```\"\n````\nafter\n"
	out := feedAll(md)
	stripped := stripANSIForTest(out)

	// The inner ``` must survive as content (still present, block didn't end there).
	if !strings.Contains(stripped, "```\"") {
		t.Errorf("inner ``` was not preserved as code content:\n%s", stripped)
	}
	if strings.Contains(out, codeGreen+"after") {
		t.Errorf("text after the block was trapped as code — inner ``` closed it early:\n%s", stripped)
	}
}

// TestCodeFenceCloseAtLeastAsLongAsOpen verifies a close fence LONGER than the
// open (5 closing a 3-open) still closes — CommonMark requires close >= open,
// not close == open.
func TestCodeFenceCloseAtLeastAsLongAsOpen(t *testing.T) {
	out := feedAll("```\ncode\n`````\nafter\n")
	if strings.Contains(out, codeGreen+"after") {
		t.Errorf("a longer closing fence failed to close a shorter-opened block:\n%s", stripANSIForTest(out))
	}
}

// TestCodeFenceThreeStillWorks guards against regression in the common 3-
// backtick path.
func TestCodeFenceThreeBackticksStillWorks(t *testing.T) {
	out := feedAll("```go\nfmt.Println()\n```\nplain text after\n")
	stripped := stripANSIForTest(out)
	if !strings.Contains(stripped, "fmt.Println()") {
		t.Errorf("3-backtick code content lost: %q", stripped)
	}
	if strings.Contains(out, codeGreen+"plain") {
		t.Errorf("text after a 3-backtick block was trapped as code:\n%s", stripped)
	}
	// Inline code after the block must still render as code.
	out2 := feedAll("```\nx\n```\n`inline` after\n")
	if !strings.Contains(out2, codeGreen+"inline") {
		t.Errorf("inline code after a block did not render as code: %q", stripANSIForTest(out2))
	}
}

// TestCodeFenceIndentedInsideList is the regression test for a fence that
// closes while INDENTED — the exact field case: a code block inside a numbered
// list item, so both its opening and closing ``` are indented 3 spaces. The
// closing fence must still be recognized (CommonMark allows a closing fence
// indented up to 3 spaces); before this, leading indentation cleared the
// "at line start" flag, so the close was missed and every following line
// (including the list's next item and its **bold**) was swept into the block.
func TestCodeFenceIndentedInsideList(t *testing.T) {
	md := "3. Query:\n" +
		"   ```\n" +
		"   object_id: { $in: [\"x\", \"*\"] }\n" +
		"   ```\n" +
		"4. **allow** or **deny**.\n"
	out := feedAll(md)
	stripped := stripANSIForTest(out)

	// The text after the block must NOT be styled as code.
	if strings.Contains(out, codeGreen+"4.") {
		t.Errorf("text after an indented-fence block was trapped as code:\n%s", stripped)
	}
	// Bold after the block must render (parser not stuck in code mode).
	if strings.Contains(stripped, "**allow**") {
		t.Errorf("**bold** after the block rendered literally — parser stuck in code mode:\n%s", stripped)
	}
	// The code content must still be present.
	if !strings.Contains(stripped, "object_id:") {
		t.Errorf("code content lost:\n%s", stripped)
	}
	// The closing fence's 3-space indentation is preserved.
	if !strings.Contains(stripped, "   ```") {
		t.Errorf("closing fence lost its indentation:\n%s", stripped)
	}
}

// TestCodeFenceIndentedFourSpacesNotAClose verifies the CommonMark boundary:
// 4+ spaces of indentation is NOT a valid closing fence (it's indented code),
// so those backticks stay literal content rather than closing the block early.
func TestCodeFenceIndentedFourSpacesNotAClose(t *testing.T) {
	// Open at col 0, then a 4-space-indented ``` (should NOT close), then a real
	// close at col 0.
	md := "```\nline1\n    ```\nstill code\n```\nafter\n"
	out := feedAll(md)
	if strings.Contains(out, codeGreen+"after") {
		t.Errorf("a 4-space-indented ``` wrongly closed the block:\n%s", stripANSIForTest(out))
	}
}
