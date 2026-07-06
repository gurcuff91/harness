package tui

import (
	"strings"

	"github.com/rivo/tview"
)

// mdState tracks markdown parse state across streaming deltas.
//
// Processed:  bold (**), italic (*), headings (#), lists (-/*), hr (---), blockquote (>)
// Code blocks (``` ... ```) and inline code (` ... `): accent+italic color,
//
//	content escaped with tview.Escape on full lines/spans.
//
// Tables (| ... |): lines buffered and escaped verbatim — no style applied.
type mdState struct {
	pending     string
	inBold      bool
	inItalic    bool
	atLineStart bool
	linePrefix  string

	// fenced code block
	inCodeBlock  bool
	codeLangDone bool // true after first line (lang label) consumed
	codeLineBuf  string
	tickBuf      string

	// inline code
	inInlineCode  bool
	inlineCodeBuf string

	// heading — track if current line is a heading (needs clrReset at \n)
	inHeading bool
	// blockquote — italic text, needs clrReset at \n
	inBlockquote bool
	// suppressNextNewline — eat the \n after --- / ___ rules
	suppressNextNewline bool

	// table — all rows buffered so we can align columns
	inTableLine  bool
	tableLineBuf string
	tableRows    []tableRow // accumulated rows until table ends
	tableIsFirst bool
}

type tableRow struct {
	cells    []string
	isHeader bool
	isSep    bool // |---|---| separator row
}

func newMdState() *mdState {
	return &mdState{atLineStart: true, tableIsFirst: true}
}

// flush drains pending state at turn_end.
func (m *mdState) flush() string {
	out := ""
	// code block — flush partial line
	if m.inCodeBlock {
		if m.codeLineBuf != "" {
			if !m.codeLangDone {
				out += tview.Escape(m.codeLineBuf) + clrReset
			} else {
				out += codeBlockLine(m.codeLineBuf)
			}
			m.codeLineBuf = ""
		}
		m.inCodeBlock = false
		m.codeLangDone = false
	}
	// inline code — flush partial span
	if m.inInlineCode {
		if m.inlineCodeBuf != "" {
			out += codeInlineSpan(m.inlineCodeBuf)
			m.inlineCodeBuf = ""
		}
		m.inInlineCode = false
	}
	// table — flush buffered rows
	if m.inTableLine {
		if m.tableLineBuf != "" {
			m.bufferTableRow(m.tableLineBuf)
			m.tableLineBuf = ""
		}
		m.inTableLine = false
	}
	if len(m.tableRows) > 0 {
		out += m.flushTable()
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
		// resolve pending stars properly instead of emitting raw
		out += m.resolveStars(0)
	}
	if m.inBold || m.inItalic || m.inHeading || m.inBlockquote {
		out += clrReset
		m.inBold = false
		m.inItalic = false
		m.inHeading = false
		m.inBlockquote = false
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
	// ── Fenced code block ────────────────────────────────────────────────
	if m.inCodeBlock {
		if ch == '`' {
			m.tickBuf += "`"
			if m.tickBuf == "```" {
				out := ""
				if m.codeLineBuf != "" {
					out += codeBlockLine(m.codeLineBuf)
					m.codeLineBuf = ""
				}
				m.tickBuf = ""
				m.inCodeBlock = false
				return out + clrDim + "```" + clrReset
			}
			return ""
		}
		if m.tickBuf != "" {
			m.codeLineBuf += m.tickBuf
			m.tickBuf = ""
		}
		if ch == '\n' {
			if !m.codeLangDone {
				// lang label line — emit in dim, close dim tag
				out := tview.Escape(m.codeLineBuf) + clrReset + "\n"
				m.codeLineBuf = ""
				m.codeLangDone = true
				return out
			}
			out := codeBlockLine(m.codeLineBuf) + "\n"
			m.codeLineBuf = ""
			return out
		}
		m.codeLineBuf += string(ch)
		return ""
	}

	// ── Inline code ──────────────────────────────────────────────────────
	if m.inInlineCode {
		if ch == '`' {
			// restore heading color after inline code span if inside a heading
			closer := clrReset
			if m.inHeading {
				closer = "[" + hexAccent + "::I]" // unset italic, keep accent color
			}
			out := codeInlineSpan(m.inlineCodeBuf) + closer
			m.inlineCodeBuf = ""
			m.inInlineCode = false
			return out
		}
		m.inlineCodeBuf += string(ch)
		return ""
	}

	// ── Table line (starts with |) ───────────────────────────────────────
	if m.inTableLine {
		if ch == '\n' {
			m.bufferTableRow(m.tableLineBuf)
			m.tableLineBuf = ""
			m.inTableLine = false
			m.atLineStart = true
			return ""
		}
		m.tableLineBuf += string(ch)
		return ""
	}

	// ── Backtick accumulation ────────────────────────────────────────────
	if ch == '`' || m.tickBuf != "" {
		if ch == '`' {
			m.tickBuf += "`"
		}
		if m.tickBuf == "```" {
			m.tickBuf = ""
			out := m.flushPending() + m.closeInline()
			m.inCodeBlock = true
			m.codeLangDone = false
			m.atLineStart = false
			return out + clrDim + "```"
		}
		if m.tickBuf == "`" && ch != '`' {
			// opening inline code
			m.tickBuf = ""
			out := m.flushPending()
			m.inInlineCode = true
			m.inlineCodeBuf = string(ch)
			return out
		}
		if len(m.tickBuf) == 2 {
			return "" // wait for third
		}
		if ch != '`' {
			t := m.tickBuf
			m.tickBuf = ""
			return t + m.processNormal(ch)
		}
		return ""
	}

	return m.processNormal(ch)
}

// renderInline renders inline markdown (bold, italic, inline-code) in a string.
// Used for table cell content.
func renderInline(s string) string {
	m := newMdState()
	m.atLineStart = false
	out := m.feed(s)
	out += m.flush()
	return out
}

// isTableSeparator returns true for lines like |---|---| or |:---|:---:|
func isTableSeparator(line string) bool {
	for _, ch := range line {
		if ch != '|' && ch != '-' && ch != ':' && ch != ' ' {
			return false
		}
	}
	return strings.ContainsRune(line, '-')
}

// splitTableCells splits a | delimited line into trimmed cells.
func splitTableCells(line string) []string {
	parts := strings.Split(line, "|")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

// bufferTableRow parses a raw table line and appends to tableRows.
func (m *mdState) bufferTableRow(line string) {
	if isTableSeparator(line) {
		m.tableRows = append(m.tableRows, tableRow{isSep: true})
		return
	}
	cells := splitTableCells(line)
	isHeader := m.tableIsFirst
	m.tableIsFirst = false
	m.tableRows = append(m.tableRows, tableRow{cells: cells, isHeader: isHeader})
}

// flushTable aligns columns and emits the full table, then resets state.
func (m *mdState) flushTable() string {
	rows := m.tableRows
	m.tableRows = nil
	m.tableIsFirst = true

	// Pre-render all cells so we measure the actual visual width
	type renderedRow struct {
		cells    []string // tview-tagged strings ready to emit
		widths   []int    // visual width of each cell
		isHeader bool
		isSep    bool
	}
	rr := make([]renderedRow, len(rows))
	for ri, row := range rows {
		rr[ri].isHeader = row.isHeader
		rr[ri].isSep = row.isSep
		if row.isSep {
			continue
		}
		rr[ri].cells = make([]string, len(row.cells))
		rr[ri].widths = make([]int, len(row.cells))
		for i, cell := range row.cells {
			var rendered string
			if row.isHeader {
				rendered = clrDim + "[::b]" + tview.Escape(cell) + clrReset
			} else {
				rendered = renderInline(cell)
			}
			rr[ri].cells[i] = rendered
			// measure visual width by stripping tview tags
			rr[ri].widths[i] = tview.TaggedStringWidth(rendered)
		}
	}

	// Compute max visual width per column across all rows
	colWidths := []int{}
	for _, row := range rr {
		if row.isSep {
			continue
		}
		for i, w := range row.widths {
			if i >= len(colWidths) {
				colWidths = append(colWidths, w)
			} else if w > colWidths[i] {
				colWidths[i] = w
			}
		}
	}

	var sb strings.Builder
	for _, row := range rr {
		if row.isSep {
			total := 0
			for _, w := range colWidths {
				total += w + 2
			}
			if len(colWidths) > 1 {
				total += len(colWidths) - 1
			}
			sb.WriteString(clrDim + strings.Repeat("─", total) + clrReset + "\n")
			continue
		}
		for i, rendered := range row.cells {
			if i > 0 {
				sb.WriteString(clrDim + " │ " + clrReset)
			}
			sb.WriteString(rendered)
			// pad to column width using visual width
			if i < len(colWidths) && i < len(row.widths) {
				pad := colWidths[i] - row.widths[i]
				if pad > 0 {
					sb.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// codeBlockLine wraps a complete buffered code line with accent+italic color.
// tview.Escape is called on the full string so tag detection works correctly.
func codeBlockLine(s string) string {
	if s == "" {
		return ""
	}
	return "[" + hexAccent + "::i]" + tview.Escape(s) + clrReset
}

// codeInlineSpan wraps inline code content with accent+italic color.
func codeInlineSpan(s string) string {
	if s == "" {
		return ""
	}
	return "[" + hexAccent + "::i]" + tview.Escape(s)
}

func (m *mdState) flushPending() string {
	if m.pending == "" {
		return ""
	}
	out := m.pending
	m.pending = ""
	return out
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
			// non-table line after table rows — flush table first
			if len(m.tableRows) > 0 {
				out += m.flushTable()
			}
			out += m.linePrefix
			m.linePrefix = ""
		}
		// close heading color before newline
		if m.inHeading {
			out += clrReset
			m.inHeading = false
		}
		// close blockquote italic before newline
		if m.inBlockquote {
			out += clrReset
			m.inBlockquote = false
		}
		m.atLineStart = true
		if m.suppressNextNewline {
			m.suppressNextNewline = false
			return out
		}
		return out + "\n"

	default:
		out := ""
		if m.pending != "" {
			out += m.resolvePending(ch)
		}
		if m.atLineStart {
			m.linePrefix += string(ch)
			// detect table line
			if m.linePrefix == "|" {
				// flush any pending stars before entering table
				out += m.flushPending()
				m.linePrefix = ""
				m.atLineStart = false
				m.inTableLine = true
				m.tableLineBuf = "|"
				return out
			}
			// first non-| char at line start after table — flush
			if len(m.tableRows) > 0 && m.linePrefix != "" && m.linePrefix[0] != '|' {
				out += m.flushTable()
			}
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

	// In blockquote, use dim-preserving tags so clrReset never kills the dim base.
	// d=set dim, B/I (uppercase)=unset bold/italic
	if m.inBlockquote {
		switch p {
		case "***":
			if m.inBold && m.inItalic {
				m.inBold, m.inItalic = false, false
				return "[::BId]" // unset bold+italic, keep dim
			}
			m.inBold, m.inItalic = true, true
			return "[::bid]"
		case "**":
			if m.inBold {
				m.inBold = false
				if m.inItalic {
					return "[::Bid]" // unset bold, keep italic+dim
				}
				return "[::Bd]" // unset bold, keep dim
			}
			m.inBold = true
			if m.inItalic {
				return "[::bid]"
			}
			return "[::bd]"
		case "*":
			if m.atLineStart && next == ' ' {
				m.atLineStart = false
				return "• "
			}
			if m.inItalic {
				m.inItalic = false
				if m.inBold {
					return "[::Ibd]" // unset italic, keep bold+dim
				}
				return "[::Id]" // unset italic, keep dim
			}
			m.inItalic = true
			if m.inBold {
				return "[::bid]"
			}
			return "[::id]"
		default:
			return p
		}
	}

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
	case s == "# ", s == "## ", s == "### ", s == "#### ":
		m.inHeading = true
		return "[" + hexAccent + "]", true
	case s == "> ":
		// note style: accent icon + dim text, bold/italic respected as-is
		m.inBlockquote = true
		return clrAccent + "󰋽 " + "[-::d]", true
	case s == "- " || s == "* ":
		return "• ", true
	case s == "---" || s == "___":
		m.suppressNextNewline = true
		return "", true
	}
	return "", false
}
