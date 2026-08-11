package components

import (
	"strings"

	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// MarkdownStream is a streaming markdown renderer. It consumes text deltas and
// emits ANSI-styled output incrementally, tracking parse state across calls.
//
// Port of the harness's tview-based renderer (transport/tui/mdrender.go),
// translated to raw ANSI. Two key simplifications over the original:
//   - ANSI needs no bracket escaping (tview required "[" -> "[[").
//   - Style emission is derived from parser state in one place (style()),
//     collapsing the duplicated normal/blockquote branches of the original.
//
// Supported: bold (**), italic (*), headings (#), lists (-/*), hr (---),
// blockquote (>), fenced code blocks (```), inline code (`), and tables (|).
type MarkdownStream struct {
	pending     string
	inBold      bool
	inItalic    bool
	atLineStart bool
	linePrefix  string

	inCodeBlock  bool
	codeLangDone bool
	codeLineBuf  string
	tickBuf      string
	// fenceLen is the backtick count of the OPENING fence of the current code
	// block (>= 3, per CommonMark). The block only closes on a fence run of at
	// least this many backticks — a shorter run is literal content. 0 when not
	// in a code block.
	fenceLen int
	// atCodeLineStart is true at the start of a line INSIDE a code block, used
	// to recognize that a backtick run is a potential closing fence (which must
	// be at line start) rather than mid-line literal backticks. Up to 3 leading
	// spaces of indentation keep it true (CommonMark allows a closing fence to
	// be indented up to 3 spaces) — codeIndentSpaces counts them so those
	// spaces are preserved as content if the run turns out NOT to be a fence.
	atCodeLineStart  bool
	codeIndentSpaces int

	inInlineCode  bool
	inlineCodeBuf string

	inHeading           bool
	headingLevel        int // 1–6, set when a heading prefix is recognized
	inBlockquote        bool
	suppressNextNewline bool

	// inStrike tracks ~~strikethrough~~ state; tildeBuf accumulates ~ chars.
	inStrike bool
	tildeBuf string

	// Link parsing: "[text](url)". linkSt tracks where we are.
	linkSt   linkState
	linkText string
	linkURL  string

	inTableLine  bool
	tableLineBuf string
	tableRows    []mdTableRow
	tableIsFirst bool

	// tableTrailingNL counts blank newlines the model emitted after a table's
	// last row. Because tables are buffered (to align columns) and only flushed
	// when the following content arrives, these newlines must be remembered and
	// re-emitted AFTER the flush so the output faithfully mirrors the model.
	tableTrailingNL int

	// width is the terminal width used to fit tables. 0 means "unconstrained"
	// (tables use their natural width, used in tests).
	width int
}

// SetWidth sets the terminal width used for width-aware table layout.
func (m *MarkdownStream) SetWidth(w int) { m.width = w }

type mdTableRow struct {
	cells    []string
	isHeader bool
	isSep    bool
}

// linkState tracks progress parsing an inline link "[text](url)".
type linkState int

const (
	linkNone    linkState = iota
	linkInText            // inside [...]
	linkBetween           // saw ], expecting (
	linkInURL             // inside (...)
)

// NewMarkdownStream creates a fresh streaming renderer (reset per turn).
func NewMarkdownStream() *MarkdownStream {
	return &MarkdownStream{atLineStart: true, tableIsFirst: true}
}

// ── Style emission (single source of truth) ─────────────────────────────────

var accentFG = func() string {
	r, g, b := 0xC8, 0xD9, 0x6A // HexAccent
	return sgrFG(r, g, b)
}()

func sgrFG(r, g, b int) string {
	return "\x1b[38;2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b) + "m"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [3]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// style returns the SGR sequence representing the current active state: a full
// reset followed by every active attribute. Wrapping (codeTracker) carries this
// across line breaks, so emitting absolute state per transition is safe.
func (m *MarkdownStream) style() string {
	s := ansi.Reset
	if m.inHeading {
		s += accentFG
		// H1/H2 are bold; H1 additionally underlined for top-level emphasis.
		if m.headingLevel <= 2 {
			s += ansi.Bold
		}
		if m.headingLevel == 1 {
			s += ansi.Under
		}
	}
	if m.inBlockquote {
		s += ansi.Dim
	}
	if m.inBold {
		s += ansi.Bold
	}
	if m.inItalic {
		s += ansi.Ital
	}
	if m.inStrike {
		s += ansi.Strike
	}
	if s == ansi.Reset {
		return ansi.Reset
	}
	return s
}

func codeBlockLine(s string) string {
	if s == "" {
		return ""
	}
	return accentFG + ansi.Ital + s + ansi.Reset
}

func codeInlineSpan(s string) string {
	if s == "" {
		return ""
	}
	return accentFG + ansi.Ital + s
}

// ── Public API ──────────────────────────────────────────────────────────────

// Feed processes a delta and returns the ANSI output produced.
func (m *MarkdownStream) Feed(delta string) string {
	var out strings.Builder
	for _, ch := range delta {
		out.WriteString(m.processChar(ch))
	}
	return out.String()
}

// Flush drains any buffered state at end of stream and returns trailing output.
func (m *MarkdownStream) Flush() string {
	out := ""
	// Incomplete link at EOF: emit the captured fragment verbatim.
	switch m.linkSt {
	case linkInText:
		out += "[" + m.linkText
	case linkBetween:
		out += "[" + m.linkText + "]"
	case linkInURL:
		out += "[" + m.linkText + "](" + m.linkURL
	}
	m.resetLink()
	// Lone trailing "~".
	if m.tildeBuf != "" {
		out += m.tildeBuf
		m.tildeBuf = ""
	}
	if m.inCodeBlock {
		// A backtick run left buffered at EOF inside a code block: if it's a
		// valid closing fence (at line start, up to 3 spaces of indentation,
		// length >= the opening fence), close the block with it; otherwise the
		// withheld indentation + run are literal code content.
		if m.tickBuf != "" {
			run := m.tickBuf
			m.tickBuf = ""
			if m.atCodeLineStart && len(run) >= m.fenceLen {
				if m.codeLineBuf != "" {
					out += codeBlockLine(m.codeLineBuf)
					m.codeLineBuf = ""
				}
				out += ansi.Dim + strings.Repeat(" ", m.codeIndentSpaces) + run + ansi.Reset
				m.codeIndentSpaces = 0
				m.inCodeBlock = false
				m.codeLangDone = false
				m.fenceLen = 0
			} else {
				m.codeLineBuf += strings.Repeat(" ", m.codeIndentSpaces) + run
				m.codeIndentSpaces = 0
			}
		} else if m.codeIndentSpaces > 0 {
			m.codeLineBuf += strings.Repeat(" ", m.codeIndentSpaces)
			m.codeIndentSpaces = 0
		}
		if m.inCodeBlock {
			if m.codeLineBuf != "" {
				if !m.codeLangDone {
					out += m.codeLineBuf + ansi.Reset
				} else {
					out += codeBlockLine(m.codeLineBuf)
				}
				m.codeLineBuf = ""
			}
			m.inCodeBlock = false
			m.codeLangDone = false
			m.fenceLen = 0
		}
	}
	if m.inInlineCode {
		if m.inlineCodeBuf != "" {
			out += codeInlineSpan(m.inlineCodeBuf)
			m.inlineCodeBuf = ""
		}
		m.inInlineCode = false
	}
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
		out += m.resolveStars(0)
	}
	if m.inBold || m.inItalic || m.inHeading || m.inBlockquote {
		out += ansi.Reset
		m.inBold, m.inItalic, m.inHeading, m.inBlockquote = false, false, false, false
	}
	return out
}

// ── Character processor ─────────────────────────────────────────────────────

func (m *MarkdownStream) processChar(ch rune) string {
	// Fenced code block.
	if m.inCodeBlock {
		// Leading indentation (up to 3 spaces) at the start of a line keeps the
		// line eligible to hold a CLOSING fence — CommonMark allows a closing
		// fence indented up to 3 spaces (common when the fence sits inside a
		// list item). Count them without clearing atCodeLineStart. A 4th space,
		// or any non-space, ends the "still at line start" window below.
		if ch == ' ' && m.atCodeLineStart && m.tickBuf == "" && m.codeIndentSpaces < 3 {
			m.codeIndentSpaces++
			return ""
		}
		if ch == '`' {
			// Accumulate the whole backtick run (don't decide at 3 — a closing
			// fence must be >= the opening fence's length, and CommonMark
			// allows longer fences). We resolve when the run ends.
			m.tickBuf += "`"
			return ""
		}
		// A backtick run just ended (ch is the first non-backtick).
		if m.tickBuf != "" {
			run := m.tickBuf
			m.tickBuf = ""
			// A closing fence must be at the START of a line (up to 3 spaces of
			// indentation allowed, tracked above) and at least as long as the
			// opening fence. Otherwise the backticks are literal content.
			if m.atCodeLineStart && len(run) >= m.fenceLen {
				out := ""
				if m.codeLineBuf != "" {
					out += codeBlockLine(m.codeLineBuf)
					m.codeLineBuf = ""
				}
				m.inCodeBlock = false
				m.fenceLen = 0
				// Preserve the closing fence's own indentation (up to 3 spaces).
				indent := strings.Repeat(" ", m.codeIndentSpaces)
				m.codeIndentSpaces = 0
				closed := out + ansi.Dim + indent + run + ansi.Reset
				// ch is the first char after the fence — reprocess it normally
				// (usually '\n'). atCodeLineStart no longer applies; we're out.
				m.atCodeLineStart = false
				return closed + m.processChar(ch)
			}
			// Not a closing fence: the leading spaces we withheld plus the run
			// are literal code content.
			if m.codeIndentSpaces > 0 {
				m.codeLineBuf += strings.Repeat(" ", m.codeIndentSpaces)
				m.codeIndentSpaces = 0
			}
			m.codeLineBuf += run
		} else if m.codeIndentSpaces > 0 {
			// Indentation spaces that weren't followed by a closing fence are
			// literal content (e.g. an indented code line).
			m.codeLineBuf += strings.Repeat(" ", m.codeIndentSpaces)
			m.codeIndentSpaces = 0
		}
		m.atCodeLineStart = false
		if ch == '\n' {
			m.atCodeLineStart = true
			m.codeIndentSpaces = 0
			if !m.codeLangDone {
				out := m.codeLineBuf + ansi.Reset + "\n"
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

	// Inline code.
	if m.inInlineCode {
		if ch == '`' {
			closer := ansi.Reset
			if m.inHeading {
				closer = accentFG // keep heading accent after code span
			}
			out := codeInlineSpan(m.inlineCodeBuf) + closer
			m.inlineCodeBuf = ""
			m.inInlineCode = false
			return out
		}
		m.inlineCodeBuf += string(ch)
		return ""
	}

	// Table line.
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

	// A buffered table ends if a new line opens with a backtick (inline code or
	// fenced block). Flush it first so the table renders above the code instead
	// of the code leaking before it. Mirrors the guard in processNormal.
	if ch == '`' && m.tickBuf == "" {
		if out := m.flushTableOnLineStart(ch); out != "" {
			return out + m.Feed(string(ch))
		}
	}

	// Backtick accumulation. We buffer the ENTIRE run of backticks and only
	// decide what it means when a non-backtick arrives: a run of >= 3 opens a
	// fenced code block (remembering the run length so the close must match or
	// exceed it — CommonMark allows fences longer than 3, used when the code
	// itself contains ```), a run of exactly 1 opens an inline code span, and
	// a run of 2 is an empty inline span (``). Deciding rigidly at 3 was the
	// old bug: a 4-backtick fence (````) opened on the first 3 and leaked the
	// 4th, desyncing the whole block.
	if ch == '`' || m.tickBuf != "" {
		// Line-start text may still be buffered in linePrefix (accumulated while
		// waiting to recognize a heading/list prefix). Emit it BEFORE the inline
		// code opens so order is preserved — otherwise the code span prints first
		// and the pending text leaks out later (e.g. "`agi` y `cm`" → "agicm y").
		pre := ""
		if ch == '`' && m.tickBuf == "" && m.atLineStart && m.linePrefix != "" {
			pre = m.emitLinePrefix()
		}
		if ch == '`' {
			m.tickBuf += "`"
			if pre != "" {
				return pre
			}
			return "" // keep accumulating until the run ends
		}

		// ch is NOT a backtick: the run has ended, resolve it by length.
		run := m.tickBuf
		m.tickBuf = ""

		if len(run) >= 3 {
			// Open a fenced code block. Resolve any pending ** / * (e.g.
			// "**```") into real bold/italic state before opening, so markers
			// never leak.
			out := m.resolvePendingStars() + m.closeInline()
			m.inCodeBlock = true
			m.codeLangDone = false
			m.fenceLen = len(run)
			m.atLineStart = false
			m.atCodeLineStart = false
			// ch is the first char of the info string (e.g. the "g" of "go")
			// or a newline — it belongs to the code block, reprocess it there.
			return pre + out + ansi.Dim + run + m.processChar(ch)
		}
		if len(run) == 1 {
			// Inline code span. ch is its first content character.
			out := m.resolvePendingStars()
			m.inInlineCode = true
			m.inlineCodeBuf = string(ch)
			return pre + out
		}
		// len(run) == 2: an empty inline code span "``" — emit nothing for it,
		// then process ch normally.
		return pre + m.processNormal(ch)
	}

	return m.processNormal(ch)
}

func (m *MarkdownStream) processNormal(ch rune) string {
	// A buffered table ends as soon as a new line opens with anything other than
	// another table row ("|"), a blank line, or leading space. Emphasis ("*"),
	// inline code ("`"), links ("["), headings, plain text — all signal the table
	// block is over. Flush it FIRST so the table renders above the following
	// content instead of that content leaking before it (and the table border
	// pasting onto the previous line). The default/prefix branch below also
	// flushes for plain text, but routes like '*', '~', '`' and links return
	// early, so we centralize the guard here to cover every entry path.
	if out := m.flushTableOnLineStart(ch); out != "" {
		return out + m.processNormal(ch)
	}
	// Inline link state machine: "[text](url)". Handled before the main switch
	// so brackets/parens are captured while a link is being parsed.
	if out, handled := m.processLink(ch); handled {
		return out
	}
	switch ch {
	case '*':
		// If we're still accumulating a line prefix (e.g. "1. " or "- "),
		// flush it as text first so emphasis that follows renders in order.
		out := ""
		if m.atLineStart && m.linePrefix != "" {
			out += m.emitLinePrefix()
		}
		m.pending += "*"
		if len(m.pending) >= 3 {
			return out + m.resolveStars(0)
		}
		return out

	case '~':
		// Accumulate ~ chars; "~~" toggles strikethrough.
		out := ""
		if m.atLineStart && m.linePrefix != "" {
			out += m.emitLinePrefix()
		}
		m.tildeBuf += "~"
		if m.tildeBuf == "~~" {
			m.tildeBuf = ""
			m.inStrike = !m.inStrike
			return out + m.style()
		}
		return out

	case '\n':
		// Blank line(s) after a table's last row, while rows are still buffered
		// and nothing is on this line: the model is emitting trailing newlines
		// before the next block. Remember them so they can be re-emitted AFTER
		// the table flushes (tables are buffered for column alignment, so we
		// can't emit these inline without breaking the table). This keeps the
		// output faithful to exactly what the model sent.
		if len(m.tableRows) > 0 && m.linePrefix == "" && m.pending == "" {
			m.tableTrailingNL++
			m.atLineStart = true
			return ""
		}
		out := m.resolveStars('\n')
		out += m.closeInline()
		if m.linePrefix != "" {
			if len(m.tableRows) > 0 {
				out += m.flushTable()
			}
			out += m.linePrefix
			m.linePrefix = ""
		}
		if m.inHeading {
			out += ansi.Reset
			m.inHeading = false
			m.headingLevel = 0
		}
		if m.inBlockquote {
			out += ansi.Reset
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
		// A lone "~" that didn't become "~~" is literal text.
		if m.tildeBuf != "" {
			out += m.tildeBuf
			m.tildeBuf = ""
		}
		if m.pending != "" {
			out += m.resolvePending(ch)
		}
		if m.atLineStart {
			m.linePrefix += string(ch)
			if m.linePrefix == "|" {
				out += m.flushPending()
				m.linePrefix = ""
				m.atLineStart = false
				m.inTableLine = true
				m.tableLineBuf = "|"
				return out
			}
			if len(m.tableRows) > 0 && m.linePrefix != "" && m.linePrefix[0] != '|' {
				out += m.flushTable()
			}
			result, consumed := m.tryLinePrefix(m.linePrefix)
			if consumed {
				m.linePrefix = ""
				m.atLineStart = false
				return out + result
			}
			// Keep accumulating until a prefix is recognizable. Longest prefixes
			// we match: "###### " (7) and "- [ ] " (6), so wait up to 7 chars.
			if len(m.linePrefix) < 7 {
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

// processLink drives the inline-link state machine for "[text](url)". It
// returns (output, handled). When handled is false the caller proceeds with
// normal processing. Renders as: underlined accent text + dim " (url)" when the
// text and URL differ, mirroring PI's non-hyperlink fallback.
func (m *MarkdownStream) processLink(ch rune) (string, bool) {
	switch m.linkSt {
	case linkNone:
		if ch == '[' {
			// Don't start a link if the current line prefix could be a task list
			// ("- ", "* ", "+ ") — the "[" belongs to "[ ]"/"[x]", not a link.
			if m.atLineStart && isBulletPrefix(m.linePrefix) {
				return "", false
			}
			// At line start the prefix buffer may hold pending text; emit it so
			// the link renders in order. (A leading "[" can't be a list/heading
			// prefix, so starting a link here is safe.)
			pre := ""
			if m.atLineStart {
				pre = m.emitLinePrefix()
			}
			m.linkSt = linkInText
			m.linkText = ""
			m.linkURL = ""
			return pre, true
		}
		return "", false

	case linkInText:
		if ch == ']' {
			m.linkSt = linkBetween
			return "", true
		}
		if ch == '\n' {
			// Not a link after all — emit the captured text verbatim.
			out := "[" + m.linkText
			m.resetLink()
			return out, false // let the newline be processed normally
		}
		m.linkText += string(ch)
		return "", true

	case linkBetween:
		if ch == '(' {
			m.linkSt = linkInURL
			return "", true
		}
		// "[text]" not followed by "(" — emit literally and reprocess this char.
		out := "[" + m.linkText + "]"
		m.resetLink()
		return out + m.processNormalText(ch), true

	case linkInURL:
		if ch == ')' {
			out := m.renderLink()
			m.resetLink()
			return out, true
		}
		if ch == '\n' {
			out := "[" + m.linkText + "](" + m.linkURL
			m.resetLink()
			return out, false
		}
		m.linkURL += string(ch)
		return "", true
	}
	return "", false
}

// renderLink formats a parsed link as a clickable OSC 8 hyperlink. The visible
// text is underlined+accent and wrapped in the hyperlink escape so supporting
// terminals open m.linkURL on click; terminals without support just show the
// styled text. The " (url)" suffix is kept (when text differs from url) so the
// destination is visible even where hyperlinks aren't clickable.
func (m *MarkdownStream) renderLink() string {
	text := m.linkText
	if text == "" {
		text = m.linkURL
	}
	styledText := accentFG + ansi.Under + text + ansi.Reset
	out := ansi.Hyperlink(styledText, m.linkURL) + m.style()
	if m.linkText != "" && m.linkText != m.linkURL {
		out += ansi.Dimmed(" ("+m.linkURL+")") + m.style()
	}
	return out
}

func (m *MarkdownStream) resetLink() {
	m.linkSt = linkNone
	m.linkText = ""
	m.linkURL = ""
}

// processNormalText feeds a single char through the normal path (used when a
// half-parsed link turns out not to be one).
func (m *MarkdownStream) processNormalText(ch rune) string {
	return m.processNormal(ch)
}

// resolveStars toggles bold/italic state from accumulated asterisks and returns
// the ANSI to apply the resulting state. Unified across normal/blockquote modes
// via style().
func (m *MarkdownStream) resolveStars(next rune) string {
	p := m.pending
	m.pending = ""
	switch p {
	case "***":
		if m.inBold && m.inItalic {
			m.inBold, m.inItalic = false, false
		} else {
			m.inBold, m.inItalic = true, true
		}
		return m.style()
	case "**":
		m.inBold = !m.inBold
		return m.style()
	case "*":
		if m.atLineStart && next == ' ' {
			m.atLineStart = false
			return "• "
		}
		m.inItalic = !m.inItalic
		return m.style()
	default:
		return p
	}
}

func (m *MarkdownStream) resolvePending(next rune) string {
	out := m.resolveStars(next)
	m.atLineStart = false
	return out
}

func (m *MarkdownStream) closeInline() string {
	if m.inBold || m.inItalic {
		m.inBold, m.inItalic = false, false
		// Preserve heading/blockquote base styling on the same line.
		return m.style()
	}
	return ""
}

func (m *MarkdownStream) flushPending() string {
	if m.pending == "" {
		return ""
	}
	out := m.pending
	m.pending = ""
	return out
}

// resolvePendingStars resolves accumulated ** / * as bold/italic toggles (not
// raw text). Used when an inline-code or fenced-code span opens while emphasis
// markers are pending, e.g. "**`code`**" — without this the ** would leak.
func (m *MarkdownStream) resolvePendingStars() string {
	if m.pending == "" {
		return ""
	}
	return m.resolveStars(0)
}

func (m *MarkdownStream) tryLinePrefix(s string) (string, bool) {
	// Headings: capture the level so style() can apply level-aware emphasis
	// (H1 bold+underline, H2 bold, H3+ plain accent). H3+ shows the # prefix.
	if lvl := headingLevel(s); lvl > 0 {
		m.inHeading = true
		m.headingLevel = lvl
		if lvl >= 3 {
			return m.style() + s, true // show "### " prefix
		}
		return m.style(), true
	}
	// Task list: "- [ ] " or "- [x] "
	if marker, ok := taskListMarker(s); ok {
		return marker, true
	}
	switch {
	case s == "> ":
		m.inBlockquote = true
		return ansi.Accent("󰋽 ") + ansi.Dim, true
	case s == "---" || s == "___" || s == "***":
		// A short, centered decorative rule — visually distinct from block
		// spacing without dominating the full terminal width. The model's
		// surrounding blank lines are preserved (no newline suppression).
		return m.renderHR(), true
	}
	// Bullets are ambiguous with task lists ("- [ ] ") and HRs ("---"). Defer
	// the plain-bullet decision until the prefix can no longer become either.
	if s == "- " || s == "* " || s == "+ " || bulletStillAmbiguous(s) {
		return "", false
	}
	// Bullet confirmed (e.g. "- x"): emit the marker plus the captured content.
	if len(s) >= 3 && (s[0] == '-' || s[0] == '*' || s[0] == '+') && s[1] == ' ' {
		return "• " + s[2:], true
	}
	// Numbered list: "<digits>. " — emit verbatim (preserves the number).
	if isNumberedListPrefix(s) {
		return s, true
	}
	return "", false
}

// isBulletPrefix reports whether s is a bullet marker that may precede a task
// list checkbox ("- ", "* ", "+ ").
func isBulletPrefix(s string) bool {
	return s == "- " || s == "* " || s == "+ "
}

// bulletStillAmbiguous reports whether s could still become a task list or HR,
// so the plain-bullet decision must wait for more characters.
func bulletStillAmbiguous(s string) bool {
	switch s {
	case "--", "__", "**",
		"- [", "* [", "+ [",
		"- [ ", "* [ ", "+ [ ",
		"- []", "- [x", "- [X", "* [x", "* [X", "+ [x", "+ [X",
		"- [ ]", "- [x]", "- [X]", "* [ ]", "* [x]", "* [X]", "+ [ ]", "+ [x]", "+ [X]":
		return true
	}
	return false
}

// renderHR builds a short, left-aligned decorative horizontal rule (max 30
// columns), so it reads as a divider rather than a full-width block separator.
func (m *MarkdownStream) renderHR() string {
	ruleLen := 30
	if m.width > 0 && m.width < ruleLen {
		ruleLen = m.width
	}
	return ansi.Dimmed(strings.Repeat("─", ruleLen))
}

// headingLevel returns 1–6 if s is a complete ATX heading prefix ("# " …
// "###### "), or 0 otherwise.
func headingLevel(s string) int {
	if len(s) < 2 || s[len(s)-1] != ' ' {
		return 0
	}
	hashes := s[:len(s)-1]
	if len(hashes) < 1 || len(hashes) > 6 {
		return 0
	}
	for i := 0; i < len(hashes); i++ {
		if hashes[i] != '#' {
			return 0
		}
	}
	return len(hashes)
}

// taskListMarker recognizes "- [ ] " / "- [x] " and returns a styled checkbox.
func taskListMarker(s string) (string, bool) {
	switch s {
	case "- [ ] ", "* [ ] ", "+ [ ] ":
		return "☐ ", true
	case "- [x] ", "* [x] ", "+ [x] ", "- [X] ", "* [X] ", "+ [X] ":
		return ansi.Accent("☑") + " ", true
	}
	return "", false
}

// isNumberedListPrefix reports whether s is a complete "N. " numbered-list
// marker (one or more digits, a dot, then a space).
func isNumberedListPrefix(s string) bool {
	if len(s) < 3 || s[len(s)-1] != ' ' || s[len(s)-2] != '.' {
		return false
	}
	for i := 0; i < len(s)-2; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// emitLinePrefix resolves the in-progress line prefix as a recognized marker
// (numbered/bulleted list, heading, etc.) or as raw text, clearing prefix state.
func (m *MarkdownStream) emitLinePrefix() string {
	if m.linePrefix == "" {
		return ""
	}
	result, consumed := m.tryLinePrefix(m.linePrefix)
	prefix := m.linePrefix
	m.linePrefix = ""
	m.atLineStart = false
	if consumed {
		return result
	}
	return prefix
}

// ── Tables ──────────────────────────────────────────────────────────────────

func isTableSeparator(line string) bool {
	for _, ch := range line {
		if ch != '|' && ch != '-' && ch != ':' && ch != ' ' {
			return false
		}
	}
	return strings.ContainsRune(line, '-')
}

func splitTableCells(line string) []string {
	parts := splitTablePipes(line)
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

// splitTablePipes splits a table row on its COLUMN-separating '|' only —
// pipes that are escaped ("\|") or that fall INSIDE an inline code span
// (delimited by backticks) are NOT column separators and stay in the cell.
// This matches GitHub-Flavored Markdown: a cell like `off|low|high` renders
// as ONE cell, not four columns. A plain strings.Split(line, "|") — what this
// replaced — was blind to both, so a model emitting an inline-code value with
// pipes (e.g. the thinking levels `off|low|medium|high|xhigh`) produced phantom
// unheadered columns.
//
// Code-span tracking mirrors CommonMark's rule loosely but sufficiently for a
// single row: a run of N backticks opens a span that only a run of exactly N
// backticks closes. That means a '|' between matched backticks is literal.
func splitTablePipes(line string) []string {
	var cells []string
	var cur strings.Builder
	runes := []rune(line)
	backtickRun := 0 // length of the code-span fence currently open; 0 = outside

	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		// Backslash-escaped pipe: emit a literal '|' and skip the escape.
		// (Only meaningful outside a code span, where '\' isn't special — but
		// treating "\|" as literal everywhere is harmless and simpler.)
		if ch == '\\' && i+1 < len(runes) && runes[i+1] == '|' {
			cur.WriteRune('|')
			i++
			continue
		}

		if ch == '`' {
			// Count the length of this backtick run.
			n := 1
			for i+n < len(runes) && runes[i+n] == '`' {
				n++
			}
			switch {
			case backtickRun == 0:
				backtickRun = n // opening a code span
			case backtickRun == n:
				backtickRun = 0 // closing it (fence lengths must match)
			}
			// Preserve the backticks verbatim so inline-code rendering still
			// sees them when the cell is rendered later.
			for k := 0; k < n; k++ {
				cur.WriteRune('`')
			}
			i += n - 1
			continue
		}

		if ch == '|' && backtickRun == 0 {
			cells = append(cells, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(ch)
	}
	cells = append(cells, cur.String())
	return cells
}

func (m *MarkdownStream) bufferTableRow(line string) {
	if isTableSeparator(line) {
		m.tableRows = append(m.tableRows, mdTableRow{isSep: true})
		return
	}
	cells := splitTableCells(line)
	isHeader := m.tableIsFirst
	m.tableIsFirst = false
	m.tableRows = append(m.tableRows, mdTableRow{cells: cells, isHeader: isHeader})
}

// renderInlineMD renders inline markdown (bold/italic/code) within a cell.
func renderInlineMD(s string) string {
	sub := NewMarkdownStream()
	sub.atLineStart = false
	return sub.Feed(s) + sub.Flush()
}

// flushTableOnLineStart flushes a buffered table when a new line begins with a
// character that cannot be part of the table block. Returns the flushed table
// output (caller then re-processes ch as the first char of the next block), or
// "" when no flush is needed. Characters that DO continue/relate to the table
// and must NOT trigger a flush here: '|' (next row), '\n' (trailing blank —
// counted elsewhere), and ' ' (potential indentation). Everything else —
// emphasis, code, links, plain text — ends the table.
func (m *MarkdownStream) flushTableOnLineStart(ch rune) string {
	if !m.atLineStart || len(m.tableRows) == 0 {
		return ""
	}
	if m.linePrefix != "" || m.pending != "" {
		return ""
	}
	switch ch {
	case '|', '\n', ' ', '\t':
		return ""
	}
	return m.flushTable()
}

// renderedTableRow is a table row with pre-rendered cells and their widths.
type renderedTableRow struct {
	cells  []string
	widths []int
	isSep  bool
}

func (m *MarkdownStream) flushTable() string {
	rows := m.tableRows
	m.tableRows = nil
	m.tableIsFirst = true

	rr := make([]renderedTableRow, len(rows))
	for ri, row := range rows {
		rr[ri].isSep = row.isSep
		if row.isSep {
			continue
		}
		rr[ri].cells = make([]string, len(row.cells))
		rr[ri].widths = make([]int, len(row.cells))
		for i, cell := range row.cells {
			var rendered string
			if row.isHeader {
				rendered = ansi.Dim + ansi.Bold + cell + ansi.Reset
			} else {
				rendered = renderInlineMD(cell)
			}
			rr[ri].cells[i] = rendered
			rr[ri].widths[i] = ansi.VisibleWidth(rendered)
		}
	}

	numCols := 0
	for _, row := range rr {
		if !row.isSep && len(row.cells) > numCols {
			numCols = len(row.cells)
		}
	}
	if numCols == 0 {
		nl := strings.Repeat("\n", m.tableTrailingNL)
		m.tableTrailingNL = 0
		return nl
	}

	colWidths := m.computeColumnWidths(rr, numCols)

	// Render with box-drawing borders and per-cell wrapping (PI strategy):
	// columns are width-constrained and cell text wraps to multiple lines.
	var sb strings.Builder
	bar := func(left, mid, right string) string {
		parts := make([]string, numCols)
		for i := 0; i < numCols; i++ {
			parts[i] = strings.Repeat("─", colWidths[i]+2)
		}
		return ansi.Dim + left + strings.Join(parts, mid) + right + ansi.Reset + "\n"
	}

	sb.WriteString(bar("┌", "┬", "┐"))
	headerDone := false
	for _, row := range rr {
		if row.isSep {
			continue
		}
		sb.WriteString(m.renderTableRow(row.cells, colWidths))
		if !headerDone {
			sb.WriteString(bar("├", "┼", "┤"))
			headerDone = true
		}
	}
	sb.WriteString(bar("└", "┴", "┘"))
	// Re-emit the blank newlines the model sent after the table's last row
	// (buffered in tableTrailingNL while the table was pending), so the output
	// faithfully reproduces the model's spacing — no more, no less.
	if m.tableTrailingNL > 0 {
		sb.WriteString(strings.Repeat("\n", m.tableTrailingNL))
		m.tableTrailingNL = 0
	}
	return sb.String()
}

// ── Table layout (port of PI's renderTable width strategy) ──────────────────

const maxUnbrokenWordWidth = 30

// computeColumnWidths sizes each column to fit within the terminal width,
// shrinking proportionally when the natural widths don't fit and falling back
// to word-aware minimums. Port of PI's renderTable column math.
func (m *MarkdownStream) computeColumnWidths(rr []renderedTableRow, numCols int) []int {
	// Natural widths (longest cell) and minimum word widths per column.
	// renderTableRow reserves 1 column of right-side padding per cell so
	// over-wide emoji glyphs can't overwrite the `│` border. We account for
	// that reservation in the border-overhead math below.
	natural := make([]int, numCols)
	minWord := make([]int, numCols)
	for i := 0; i < numCols; i++ {
		minWord[i] = 1
	}
	for _, row := range rr {
		if row.isSep {
			continue
		}
		for i := 0; i < numCols && i < len(row.cells); i++ {
			w := row.widths[i]
			if w > natural[i] {
				natural[i] = w
			}
			if lw := longestWordWidth(row.cells[i], maxUnbrokenWordWidth); lw > minWord[i] {
				minWord[i] = lw
			}
		}
	}

	// Unconstrained (width 0, e.g. tests): use natural widths. Each column
	// gets +1 for the right-side safety margin (see renderTableRow) so a cell
	// that's exactly the natural width still leaves room for an over-wide
	// emoji glyph and a trailing space before the right border.
	if m.width <= 0 {
		out := make([]int, numCols)
		for i := range out {
			out[i] = natural[i] + 1
		}
		return out
	}

	// borderOverhead accounts for:
	//   - +3 per column for the per-cell border characters (left pipe + space +
	//     right pipe+space split between adjacent columns).
	//   - +numCols for the 1-column right-side safety margin per cell (the pad
	//     that keeps over-wide emoji glyphs from overwriting the `│` border —
	//     see renderTableRow).
	//   - +1 for the final right edge "│" that closes the table.
	borderOverhead := 4*numCols + 1
	availableForCells := m.width - borderOverhead
	if availableForCells < numCols {
		// Too narrow for a stable grid — clamp to 1 col each (caller still
		// renders; the differential layer will truncate over-wide lines).
		availableForCells = numCols
	}

	// Minimum widths: prefer word-aware mins, but if they don't fit, fall back
	// to 1 each plus proportional growth.
	minCol := make([]int, numCols)
	copy(minCol, minWord)
	minSum := sum(minCol)
	if minSum > availableForCells {
		for i := range minCol {
			minCol[i] = 1
		}
		remaining := availableForCells - numCols
		if remaining > 0 {
			totalWeight := 0
			for _, w := range minWord {
				totalWeight += max(0, w-1)
			}
			alloc := 0
			for i := 0; i < numCols; i++ {
				if totalWeight > 0 {
					g := (max(0, minWord[i]-1) * remaining) / totalWeight
					minCol[i] += g
					alloc += g
				}
			}
			for i := 0; alloc < remaining && i < numCols; i++ {
				minCol[i]++
				alloc++
			}
		}
		minSum = sum(minCol)
	}

	totalNatural := sum(natural) + borderOverhead
	if totalNatural <= m.width {
		// Everything fits naturally.
		out := make([]int, numCols)
		for i := range out {
			out[i] = max(natural[i], minCol[i])
		}
		return out
	}

	// Shrink: distribute the extra space proportionally to grow potential.
	growPotential := 0
	for i := range natural {
		growPotential += max(0, natural[i]-minCol[i])
	}
	extra := max(0, availableForCells-minSum)
	out := make([]int, numCols)
	for i := range out {
		delta := max(0, natural[i]-minCol[i])
		grow := 0
		if growPotential > 0 {
			grow = (delta * extra) / growPotential
		}
		out[i] = minCol[i] + grow
	}
	// Distribute rounding leftover.
	allocated := sum(out)
	for remaining := availableForCells - allocated; remaining > 0; {
		grew := false
		for i := 0; i < numCols && remaining > 0; i++ {
			if out[i] < natural[i] {
				out[i]++
				remaining--
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	return out
}

// renderTableRow renders one row with per-cell wrapping, producing as many
// physical lines as the tallest wrapped cell.
func (m *MarkdownStream) renderTableRow(cells []string, colWidths []int) string {
	numCols := len(colWidths)
	wrapped := make([][]string, numCols)
	maxLines := 1
	for i := 0; i < numCols; i++ {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		// Wrap at colWidths[i]. Any leftover space is filled with spaces below
		// so the cell flushes to colWidths[i] — keeping every row aligned. The
		// 1-column right-side safety margin (which keeps over-wide emoji
		// glyphs from overwriting the `│` border) is baked into colWidths[i]
		// by computeColumnWidths' borderOverhead accounting.
		wrapped[i] = ansi.WrapTextWithAnsi(cell, colWidths[i])
		if len(wrapped[i]) > maxLines {
			maxLines = len(wrapped[i])
		}
	}

	var sb strings.Builder
	sep := ansi.Dim + " │ " + ansi.Reset
	edge := ansi.Dim + "│" + ansi.Reset
	for li := 0; li < maxLines; li++ {
		sb.WriteString(edge + " ")
		for i := 0; i < numCols; i++ {
			text := ""
			if li < len(wrapped[i]) {
				text = wrapped[i][li]
			}
			// Pad to colWidths[i] so every cell flushes. colWidths[i] already
			// reserves 1 column of right-side padding (the safety margin that
			// stops over-wide emoji glyphs from clobbering the border), so
			// short cells get enough trailing space to stay flush with long
			// cells in the same column.
			pad := colWidths[i] - ansi.VisibleWidth(text)
			if pad < 0 {
				pad = 0
			}
			sb.WriteString(text + strings.Repeat(" ", pad))
			if i < numCols-1 {
				sb.WriteString(sep)
			}
		}
		sb.WriteString(" " + edge + "\n")
	}
	return sb.String()
}

// longestWordWidth returns the visible width of the longest whitespace-separated
// word in text, capped at maxWidth.
func longestWordWidth(text string, maxWidth int) int {
	longest := 0
	for _, word := range strings.Fields(text) {
		if w := ansi.VisibleWidth(word); w > longest {
			longest = w
		}
	}
	if maxWidth > 0 && longest > maxWidth {
		return maxWidth
	}
	return longest
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}
