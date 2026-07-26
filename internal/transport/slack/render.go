package slack

import (
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/internal/client"
)

// slackMaxLen is Slack's per-message character cap. Longer replies are split.
const slackMaxLen = 4000

// toMrkdwn converts CommonMark output from the agent into Slack mrkdwn.
//
// Translation rules:
//   - Fenced code blocks (```lang…```) → passed through verbatim (Slack renders them)
//   - Inline `code` → passed through verbatim
//   - **bold** / __bold__  → *bold*   (Slack bold uses single asterisk)
//   - *italic* / _italic_  → _italic_ (Slack italic uses underscore)
//   - # Heading (any level) → *Heading* (bold line, Slack has no headings)
//   - --- / *** / ___ (HR)  → removed  (Slack has no horizontal rules)
//   - - item / * item       → • item   (proper bullet)
//
// Slack mrkdwn does NOT require escaping regular characters, so we don't need
// the heavy escaper that Telegram MarkdownV2 requires.
func toMrkdwn(md string) string {
	// Slack has no table support — rewrite pipe tables into aligned code blocks
	// (monospace, same approach as Telegram).
	md = tablesToCodeBlocks(md)

	var out strings.Builder
	lines := strings.Split(md, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Fenced code block toggle.
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out.WriteString(trimmed)
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
			continue
		}
		if inFence {
			// Inside a code block — pass through verbatim.
			out.WriteString(line)
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
			continue
		}

		// Horizontal rule (---, ***, ___) → drop completely (no replacement,
		// not even a blank line — the surrounding blank lines are enough).
		if isHorizontalRule(trimmed) {
			continue
		}

		// Heading → *bold line*.
		if h := stripHeading(trimmed); h != "" {
			out.WriteByte('*')
			out.WriteString(renderMrkdwnInline(h))
			out.WriteByte('*')
		} else {
			out.WriteString(renderMrkdwnInline(line))
		}
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	result := out.String()
	// Collapse runs of 2+ consecutive blank lines to a single blank line.
	// This cleans up the double-spacing left when --- is removed from between
	// sections that already have blank lines around them.
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return result
}

// isHorizontalRule reports whether a line is a markdown horizontal rule.
func isHorizontalRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	c := line[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for _, r := range line {
		if r != rune(c) && r != ' ' {
			return false
		}
	}
	return true
}

// stripHeading returns the heading text (after the # prefix), or "" if the line
// is not a heading.
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

// renderMrkdwnInline translates inline CommonMark markers to Slack mrkdwn.
// Slack does not require escaping of non-marker characters, so we only
// rewrite the markdown spans we recognise.
func renderMrkdwnInline(line string) string {
	// Convert list markers at the start of the line.
	if t := strings.TrimLeft(line, " \t"); len(t) >= 2 {
		prefix := t[:2]
		if prefix == "- " || prefix == "* " {
			indent := line[:len(line)-len(t)]
			return indent + "• " + renderMrkdwnSpans(t[2:])
		}
	}
	return renderMrkdwnSpans(line)
}

// renderMrkdwnSpans handles inline bold/italic/code spans.
func renderMrkdwnSpans(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '`':
			// Inline code — pass through to closing backtick.
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				out.WriteByte(c)
				i++
				continue
			}
			out.WriteByte('`')
			out.WriteString(s[i+1 : i+1+end])
			out.WriteByte('`')
			i += end + 2

		case c == '*' && i+1 < len(s) && s[i+1] == '*':
			// **bold** → *bold*
			end := strings.Index(s[i+2:], "**")
			if end < 0 {
				out.WriteString("**")
				i += 2
				continue
			}
			out.WriteByte('*')
			out.WriteString(renderMrkdwnSpans(s[i+2 : i+2+end]))
			out.WriteByte('*')
			i += end + 4

		case c == '_' && i+1 < len(s) && s[i+1] == '_':
			// __bold__ → *bold*
			end := strings.Index(s[i+2:], "__")
			if end < 0 {
				out.WriteString("__")
				i += 2
				continue
			}
			out.WriteByte('*')
			out.WriteString(renderMrkdwnSpans(s[i+2 : i+2+end]))
			out.WriteByte('*')
			i += end + 4

		case (c == '*' || c == '_') && i+1 < len(s) && s[i+1] != c && s[i+1] != ' ':
			// Single *italic* or _italic_ → _italic_
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 || (end > 0 && s[i+1+end-1] == ' ') {
				out.WriteByte(c)
				i++
				continue
			}
			out.WriteByte('_')
			out.WriteString(renderMrkdwnSpans(s[i+1 : i+1+end]))
			out.WriteByte('_')
			i += end + 2

		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// formatError renders an error for Slack: the message first, then structured
// details in a code block if present.
func formatError(msg string, details map[string]any) string {
	if len(details) == 0 {
		return "⚠️ " + msg
	}
	var b strings.Builder
	b.WriteString("⚠️ ")
	b.WriteString(msg)
	b.WriteString("\n```\n")
	for k, v := range details {
		fmt.Fprintf(&b, "%s: %v\n", k, v)
	}
	b.WriteString("```")
	return b.String()
}

// formatAgentError formats a *client.Error for Slack.
func formatAgentError(ae *client.Error) string {
	return formatError(ae.Message, ae.Details)
}

// splitMessage splits text into chunks that fit Slack's 4000-char limit,
// breaking on newlines when possible.
func splitMessage(text string) []string {
	if len(text) <= slackMaxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > slackMaxLen {
		cut := slackMaxLen
		// Try to break at a newline within the last 200 chars of the chunk.
		if i := strings.LastIndexByte(text[:slackMaxLen], '\n'); i > slackMaxLen-200 {
			cut = i + 1
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

// oneLine collapses text to one line and truncates for log entries.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
