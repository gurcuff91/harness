package tui

import "strings"

// mdState tracks markdown parse state across streaming deltas.
// Code blocks (``` ... ```) and inline code (` ... `) are NOT processed —
// they pass through verbatim so the model's output is never modified.
type mdState struct {
	pending     string
	inBold      bool
	inItalic    bool
	atLineStart bool
	linePrefix  string
}

func newMdState() *mdState {
	return &mdState{atLineStart: true}
}

// flush drains any pending state at turn_end.
func (m *mdState) flush() string {
	out := ""
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
		return "[" + clrPrimaryHex + "][::b]", true
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

const (
	clrPrimaryHex = "#26A69A"
	clrAccentHex  = "#C8D96A"
)
