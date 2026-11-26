package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// A deliberately small Markdown subset — what the documentation uses:
// headings, paragraphs, bullet/numbered lists, tables, fenced code, block
// quotes (callouts), and inline code, bold, italic, links.

type heading struct {
	Level int
	ID    string
	Text  string
}

var (
	reCode   = regexp.MustCompile("`([^`]+)`")
	reBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic = regexp.MustCompile(`(^|[\s(])\*([^*\s][^*]*)\*`)
	reLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reNum    = regexp.MustCompile(`^\d+\.\s+`)
	reSep    = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)
)

// slug makes a heading's anchor.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// inline renders the span-level syntax of one line of text. Code spans are
// lifted out first so nothing inside them is interpreted.
func inline(s string, link func(string) string) string {
	var codes []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, "<code>"+html.EscapeString(m[1:len(m)-1])+"</code>")
		return fmt.Sprintf("\x00%d\x00", len(codes)-1)
	})
	s = html.EscapeString(s)
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "$1<em>$2</em>")
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		p := reLink.FindStringSubmatch(m)
		return `<a href="` + link(html.UnescapeString(p[2])) + `">` + p[1] + `</a>`
	})
	for i, c := range codes {
		s = strings.Replace(s, fmt.Sprintf("\x00%d\x00", i), c, 1)
	}
	return s
}

// callout kinds: a block quote starting with "**Tip**", "**Note**" or "**Warning**".
func calloutKind(first string) (kind, rest string) {
	for _, k := range []string{"Tip", "Note", "Warning"} {
		p := "**" + k + "**"
		if strings.HasPrefix(first, p) {
			return strings.ToLower(k), strings.TrimSpace(strings.TrimPrefix(first, p))
		}
	}
	return "", first
}

func renderMarkdown(src string, link func(string) string) (string, []heading) {
	var out strings.Builder
	var heads []heading
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	used := map[string]int{}

	for i := 0; i < len(lines); {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			i++

		case strings.HasPrefix(trim, "```"):
			lang := strings.TrimSpace(strings.TrimPrefix(trim, "```"))
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			i++
			out.WriteString(codeBlock(lang, code))

		case strings.HasPrefix(trim, "#"):
			level := len(trim) - len(strings.TrimLeft(trim, "#"))
			if level > 4 || level >= len(trim) || trim[level] != ' ' {
				goto para
			}
			text := strings.TrimSpace(trim[level:])
			id := slug(text)
			if n := used[id]; n > 0 {
				id = fmt.Sprintf("%s-%d", id, n)
			}
			used[slug(text)]++
			if level == 2 || level == 3 {
				heads = append(heads, heading{level, id, text})
			}
			fmt.Fprintf(&out, "<h%d id=%q>%s</h%d>\n", level, id, inline(text, link), level)
			i++

		case strings.HasPrefix(trim, ">"):
			var q []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				q = append(q, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
				i++
			}
			kind, first := calloutKind(strings.Join(q, " "))
			if kind == "" {
				fmt.Fprintf(&out, "<blockquote><p>%s</p></blockquote>\n", inline(first, link))
			} else {
				fmt.Fprintf(&out, "<div class=\"callout %s\"><b>%s</b> %s</div>\n", kind, strings.ToUpper(kind[:1])+kind[1:], inline(first, link))
			}

		case strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") || reNum.MatchString(trim):
			ordered := reNum.MatchString(trim)
			tag := "ul"
			if ordered {
				tag = "ol"
			}
			fmt.Fprintf(&out, "<%s>\n", tag)
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				var item string
				switch {
				case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
					item = t[2:]
				case reNum.MatchString(t):
					item = reNum.ReplaceAllString(t, "")
				default:
					goto endList
				}
				i++
				// continuation lines (indented, not a new item)
				for i < len(lines) && strings.HasPrefix(lines[i], "  ") && strings.TrimSpace(lines[i]) != "" &&
					!strings.HasPrefix(strings.TrimSpace(lines[i]), "- ") && !reNum.MatchString(strings.TrimSpace(lines[i])) {
					item += " " + strings.TrimSpace(lines[i])
					i++
				}
				fmt.Fprintf(&out, "<li>%s</li>\n", inline(item, link))
			}
		endList:
			fmt.Fprintf(&out, "</%s>\n", tag)

		case strings.HasPrefix(trim, "|") && i+1 < len(lines) && reSep.MatchString(strings.TrimSpace(lines[i+1])):
			head := splitRow(trim)
			i += 2
			out.WriteString("<div class=\"table\"><table>\n<tr>")
			for _, h := range head {
				out.WriteString("<th>" + inline(h, link) + "</th>")
			}
			out.WriteString("</tr>\n")
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
				out.WriteString("<tr>")
				for _, c := range splitRow(strings.TrimSpace(lines[i])) {
					out.WriteString("<td>" + inline(c, link) + "</td>")
				}
				out.WriteString("</tr>\n")
				i++
			}
			out.WriteString("</table></div>\n")

		default:
			goto para
		}
		continue

	para:
		{
			var p []string
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !startsBlock(strings.TrimSpace(lines[i])) {
				p = append(p, strings.TrimSpace(lines[i]))
				i++
			}
			if len(p) == 0 { // a lone line that looks like a block start but is not one
				p = append(p, strings.TrimSpace(lines[i]))
				i++
			}
			fmt.Fprintf(&out, "<p>%s</p>\n", inline(strings.Join(p, " "), link))
		}
	}
	return out.String(), heads
}

func startsBlock(t string) bool {
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") ||
		strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || reNum.MatchString(t) || strings.HasPrefix(t, "|")
}

func splitRow(r string) []string {
	r = strings.TrimSpace(r)
	r = strings.TrimPrefix(r, "|")
	r = strings.TrimSuffix(r, "|")
	var cells []string
	for _, c := range strings.Split(r, "|") {
		cells = append(cells, strings.TrimSpace(c))
	}
	return cells
}

// codeBlock renders a fenced block. A "bash"/"sh" block gets the prompt and
// comment colors of the TUI palette; everything else is plain.
func codeBlock(lang string, code []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<pre data-lang=%q><code>", lang)
	for n, l := range code {
		if n > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(highlight(lang, l))
	}
	b.WriteString("</code></pre>\n")
	return b.String()
}

func highlight(lang, l string) string {
	e := html.EscapeString(l)
	switch lang {
	case "bash", "sh", "shell":
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "#"):
			return `<span class="c">` + e + `</span>`
		case strings.HasPrefix(t, "$ "):
			i := strings.Index(e, "$ ")
			rest := e[i+2:]
			if j := strings.Index(rest, " # "); j >= 0 {
				rest = rest[:j] + `<span class="c">` + rest[j:] + `</span>`
			}
			return e[:i] + `<span class="k">$</span> ` + rest
		}
		if j := strings.Index(e, " # "); j >= 0 {
			return e[:j] + `<span class="c">` + e[j:] + `</span>`
		}
	case "go":
		if j := strings.Index(e, "//"); j >= 0 {
			return e[:j] + `<span class="c">` + e[j:] + `</span>`
		}
	case "json":
		return e
	}
	return e
}
