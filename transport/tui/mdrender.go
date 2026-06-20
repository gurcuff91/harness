package tui

import "strings"

// mdState tracks markdown parse state across streaming deltas.
// Code blocks (``` ... ```) are rendered with accent+italic color.
// Each line is buffered and [ is escaped to [[ before emitting so
// tview never interprets code content as color/style tags.
type mdState struct {
	pending     string
	inBold      bool
	inItalic    bool
	inCodeBlock bool   // fenced ```block```
	codeLineBuf string // buffer current line inside code block
	tickBuf     string // accumulate backticks to detect ```
	atLineStart bool
	linePrefix  string
}

func newMdState() *mdState {
	return &mdState{atLineStart: true}
}

// flush drains any pending state at turn_end.
func (m *mdState) flush() string {
	out := ""
	if m.inCodeBlock {
		if m.codeLineBuf != "" {
			out += codeLine(m.codeLineBuf)
			m.codeLineBuf = ""
		}
		m.inCodeBlock = false
	}
	if m.tickBuf != "" {
		out += m.tickBuf
		m.tickBuf = ""
	}
	if m.linePrefix != "" {
		out += m.linePrefix
		m.linePrefix = ""
	}
	if m.pending != "" {
		out += m.pending
		m.pending = ""
	}
	if m.inBold || m.inItalic {
		out += clrReset
		m.inBold = false
		m.inItalic = false
	}
	return out
}

// feed processes a streaming delta and returns tview-marked-up output.
func (m *mdState) feed(delta string) string {
	var out strings.Builder
	for _, ch := range delta {
		out.WriteString(m.processChar(ch))
	}
	return out.String()
}

func (m *mdState) processChar(ch rune) string {
	// ── Inside fenced code block ───────────────────────────────────────
	if m.inCodeBlock {
		if ch == '`' {
			m.tickBuf += "`"
			if m.tickBuf == "```" {
				// closing fence — flush remaining line buf
				out := ""
				if m.codeLineBuf != "" {
					out += codeLine(m.codeLineBuf)
					m.codeLineBuf = ""
				}
				m.tickBuf = ""
				m.inCodeBlock = false
				return out + "```"
			}
			return ""
		}
		// non-backtick: flush any partial tickBuf into line buf
		if m.tickBuf != "" {
			m.codeLineBuf += m.tickBuf
			m.tickBuf = ""
		}
		if ch == '\n' {
			out := codeLine(m.codeLineBuf) + "\n"
			m.codeLineBuf = ""
			return out
		}
		m.codeLineBuf += string(ch)
		return ""
	}

	// ── Backtick accumulation (detect opening ```) ────────────────────
	if ch == '`' || m.tickBuf != "" {
		if ch == '`' {
			m.tickBuf += "`"
		}
		if m.tickBuf == "```" {
			m.tickBuf = ""
			out := m.pending + m.closeInline()
			m.pending = ""
			m.inCodeBlock = true
			m.atLineStart = false
			return out + "```"
		}
		if len(m.tickBuf) < 3 && ch == '`' {
			return ""
		}
		// not a ``` — emit tickBuf verbatim + process current char normally
		t := m.tickBuf
		m.tickBuf = ""
		if ch != '`' {
			return t + m.processNormal(ch)
		}
		return t
	}

	return m.processNormal(ch)
}

func (m *mdState) processNormal(ch rune) string {
	switch ch {
	case '*':
		m.pending += "*"
		if len(m.pending) >= 3 {
			return m.resolveStars(0)
		}
		return ""

	case '\n':
		out := m.resolveStars('\n')
		out += m.closeInline()
		if m.linePrefix != "" {
			out += m.linePrefix
			m.linePrefix = ""
		}
		m.atLineStart = true
		return out + "\n"

	default:
		out := ""
		if m.pending != "" {
			out += m.resolvePending(ch)
		}
		if m.atLineStart {
			m.linePrefix += string(ch)
			result, consumed := m.tryLinePrefix(m.linePrefix)
			if consumed {
				m.linePrefix = ""
				m.atLineStart = false
				return out + result
			}
			if len(m.linePrefix) < 5 {
				return out
			}
			m.atLineStart = false
			prefix := m.linePrefix
			m.linePrefix = ""
			return out + prefix
		}
		return out + string(ch)
	}
}

func (m *mdState) resolveStars(next rune) string {
	p := m.pending
	m.pending = ""
	switch p {
	case "***":
		if m.inBold && m.inItalic {
			m.inBold, m.inItalic = false, false
			return clrReset
		}
		m.inBold, m.inItalic = true, true
		return "[::bi]"
	case "**":
		if m.inBold {
			m.inBold = false
			if m.inItalic {
				return "[::i]"
			}
			return clrReset
		}
		m.inBold = true
		if m.inItalic {
			return "[::bi]"
		}
		return "[::b]"
	case "*":
		if m.atLineStart && next == ' ' {
			m.atLineStart = false
			return "• "
		}
		if m.inItalic {
			m.inItalic = false
			if m.inBold {
				return "[::b]"
			}
			return clrReset
		}
		m.inItalic = true
		if m.inBold {
			return "[::bi]"
		}
		return "[::i]"
	default:
		return p
	}
}

func (m *mdState) resolvePending(next rune) string {
	out := m.resolveStars(next)
	m.atLineStart = false
	return out
}

func (m *mdState) closeInline() string {
	if m.inBold || m.inItalic {
		m.inBold, m.inItalic = false, false
		return clrReset
	}
	return ""
}

func (m *mdState) tryLinePrefix(s string) (string, bool) {
	switch {
	case s == "# ":
		return "[" + hexPrimary + "][::b]", true
	case s == "## ":
		return "[::b]", true
	case s == "### ":
		return "[::bi]", true
	case s == "#### ":
		return "[::b]", true
	case s == "> ":
		return "[::d]▎ ", true
	case s == "- " || s == "* ":
		return "• ", true
	case s == "---" || s == "___":
		return clrDim + "────────────────────────────" + clrReset, true
	}
	return "", false
}

// codeLine returns the line as-is. No color, no escaping.
// Code content is passed verbatim — tview may eat some [word] patterns
// but that is acceptable vs corrupting the output.
func codeLine(s string) string {
	return s
}


