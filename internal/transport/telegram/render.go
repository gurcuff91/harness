package telegram

import "strings"

// telegramMaxLen is Telegram's per-message character cap. Longer replies are
// split across multiple messages.
const telegramMaxLen = 4096

// toMarkdownV2 converts the agent's CommonMark output into Telegram MarkdownV2.
// Telegram's dialect is strict: every one of `_*[]()~`>#+-=|{}.!` must be
// escaped outside of an entity, and it has no headings. Sending the agent's raw
// markdown would trigger 400 errors, so we translate pragmatically:
//
//   - fenced code blocks (```lang\n…\n```) are preserved verbatim (only \ and `
//     escaped inside), which is exactly MarkdownV2's pre syntax
//   - inline `code` is preserved (escaping \ and ` inside)
//   - **bold** / __bold__ → *bold*, *italic*/_italic_ → _italic_
//   - # headings → *bold* line
//   - every other special char is backslash-escaped
//
// The caller still falls back to plain text if Telegram rejects the result.
func toMarkdownV2(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			// Fence delimiter: pass the ``` and any language through untouched.
			inFence = !inFence
			out.WriteString(trimmed)
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
			continue
		}
		if inFence {
			out.WriteString(escapeCode(line))
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
			continue
		}
		// Heading → bold line.
		if h := stripHeading(trimmed); h != "" {
			out.WriteString("*")
			out.WriteString(escapeInline(h))
			out.WriteString("*")
		} else {
			out.WriteString(renderInline(line))
		}
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// stripHeading returns the text of a markdown heading line (after the #s), or ""
// if the line isn't a heading.
func stripHeading(line string) string {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n >= len(line) || line[n] != ' ' {
		return ""
	}
	return strings.TrimSpace(line[n+1:])
}

// renderInline processes one line, preserving inline `code` spans and
// **bold**/*italic* while escaping everything else for MarkdownV2.
//
// It scans by byte, but the markdown markers (`*_`) are all ASCII and every
// byte of a multi-byte UTF-8 rune has its high bit set, so they never collide.
// The default branch writes the byte verbatim (WriteByte, never string(byte),
// which would mangle multi-byte runes into mojibake).
func renderInline(line string) string {
	var out strings.Builder
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == '`':
			// Inline code span: copy verbatim (code-escaped) until the closing `.
			end := strings.IndexByte(line[i+1:], '`')
			if end < 0 {
				// No close — treat as a literal backtick.
				out.WriteString("\\`")
				i++
				continue
			}
			out.WriteByte('`')
			out.WriteString(escapeCode(line[i+1 : i+1+end]))
			out.WriteByte('`')
			i += end + 2
		case c == '*' && i+1 < len(line) && line[i+1] == '*':
			// **bold** → *bold*
			end := strings.Index(line[i+2:], "**")
			if end < 0 {
				out.WriteString("\\*")
				i++
				continue
			}
			out.WriteByte('*')
			out.WriteString(escapeInline(line[i+2 : i+2+end]))
			out.WriteByte('*')
			i += end + 4
		case c == '_' && i+1 < len(line) && line[i+1] == '_':
			// __bold__ → *bold*
			end := strings.Index(line[i+2:], "__")
			if end < 0 {
				out.WriteString("\\_")
				i++
				continue
			}
			out.WriteByte('*')
			out.WriteString(escapeInline(line[i+2 : i+2+end]))
			out.WriteByte('*')
			i += end + 4
		case c == '*' || c == '_':
			// Single *italic* or _italic_ (CommonMark) → _italic_ (MarkdownV2).
			// The ** / __ cases above already handled bold, so a lone marker here is
			// italic. Require a matching close on the same line and a non-space just
			// inside, else treat as a literal (escaped) character.
			if inner := italicSpan(line[i:], c); inner >= 0 {
				out.WriteByte('_')
				out.WriteString(escapeInline(line[i+1 : i+1+inner]))
				out.WriteByte('_')
				i += inner + 2
			} else {
				out.WriteByte('\\')
				out.WriteByte(c)
				i++
			}
		default:
			if isSpecial(c) {
				out.WriteByte('\\')
			}
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// italicSpan returns the length of an italic span's inner text given s starting
// at the opening marker (s[0]==marker), or -1 if s isn't a well-formed italic
// run. A valid run has a matching marker later on the line with non-space text
// between them (so "2 * 3" or a stray "*" isn't mistaken for italics).
func italicSpan(s string, marker byte) int {
	if len(s) < 3 || s[1] == ' ' {
		return -1
	}
	close := strings.IndexByte(s[1:], marker)
	if close <= 0 {
		return -1
	}
	// No space immediately before the closing marker.
	if s[close] == ' ' {
		return -1
	}
	return close
}

// markdownV2Specials are the characters MarkdownV2 requires escaped in normal text.
// All are ASCII (< 0x80), so they never appear inside a multi-byte UTF-8 rune.
const markdownV2Specials = "_*[]()~`>#+-=|{}.!\\"

// isSpecial reports whether an ASCII byte must be escaped in MarkdownV2 body text.
func isSpecial(c byte) bool {
	return strings.IndexByte(markdownV2Specials, c) >= 0
}

// escapeInline escapes a run of plain text for MarkdownV2 body context. It scans
// by byte and writes each byte verbatim (WriteByte) so multi-byte UTF-8 runes
// — accents, ñ, emoji — pass through intact; only ASCII specials get a backslash.
func escapeInline(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isSpecial(c) {
			out.WriteByte('\\')
		}
		out.WriteByte(c)
	}
	return out.String()
}

// escapeCode escapes text inside a code span/block: only \ and ` are special.
func escapeCode(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// splitMessage breaks text into chunks no longer than telegramMaxLen, preferring
// to split on paragraph, then line, then hard boundaries so entities aren't torn
// mid-token when possible.
func splitMessage(text string) []string {
	if len(text) <= telegramMaxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > telegramMaxLen {
		cut := lastBoundary(text, telegramMaxLen)
		chunks = append(chunks, strings.TrimRight(text[:cut], "\n"))
		text = strings.TrimLeft(text[cut:], "\n")
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

// lastBoundary finds the best index (<= max) to cut text: a double newline, then
// a single newline, then a space; falling back to max if none exist.
func lastBoundary(text string, max int) int {
	window := text[:max]
	if i := strings.LastIndex(window, "\n\n"); i > 0 {
		return i
	}
	if i := strings.LastIndexByte(window, '\n'); i > 0 {
		return i
	}
	if i := strings.LastIndexByte(window, ' '); i > 0 {
		return i
	}
	return max
}
