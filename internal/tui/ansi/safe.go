package ansi

import (
	"strings"
	"unicode/utf8"
)

// tabSpaces is what a TAB becomes on screen (tabWidth spaces).
var tabSpaces = strings.Repeat(" ", tabWidth)

// SafeLine is the barrier between text the TUI does not control and the host terminal.
//
// Everything the TUI draws ends up as lines handed to the terminal, and a lot of it is not ours:
// the user's prompt (typed or pasted), the model's reply and reasoning, a tool's arguments and
// output, a provider's error text, a session's name. Written as-is, an ESC in any of them would be
// INTERPRETED by the terminal instead of shown — OSC 52 writes the clipboard, OSC 0 retitles the
// window, CSI 6n makes the terminal answer on stdin as if the user had typed it, CSI ?1049h
// switches screens, DCS/APC carry arbitrary payloads. (The width helpers only MEASURE; none of
// them cleans.)
//
// So a line is let through only if it contains nothing but what this TUI emits ITSELF, and anything
// else is neutralized rather than guessed at. An allow-list, not a block-list: a terminal has far
// more sequences than anyone can enumerate, and a new one must be safe by default.
//
// Kept, byte for byte:
//   - SGR (ESC [ … m) made only of the attributes and colors the TUI uses (see allowedSGR) — and not
//     conceal (8), blink (5, 6) or anything unknown: SGR is styling, but conceal is text the user
//     cannot see, and nothing here has a use for it.
//   - CSI K, the erase-to-end-of-line the components use.
//   - OSC 8 hyperlinks (open and close) whose URL passes SafeURL.
//
// Expanded:
//   - TAB becomes tabWidth spaces — exactly what the width helpers count it as. A terminal moves a
//     TAB to its next tab stop (every 8 columns), so a line holding one is WIDER on screen than it
//     was measured: the terminal wraps it on its own, every row below shifts by one, and the diff
//     renderer then redraws its "changed" lines on the wrong rows (seen live: the editor's border
//     smeared into a Bash tool's body whose command had tabs in it).
//
// Neutralized:
//   - Any other escape sequence: its ESC (0x1B) is replaced by the visible symbol U+241B "␛" and the
//     rest of the text is kept, so what was there stays readable and nothing is silently hidden.
//   - Every other control character — C0, DEL, and the C1 range, whose single
//     characters U+0080–U+009F are the 8-bit forms of CSI/OSC/DCS/ST: dropped, except that BEL is
//     shown as "␇" (U+2407) for the same reason as the ESC. A bare CR or LF cannot occur in a line
//     (lines are split on \n and a CR moves the cursor), so they are dropped too.
//   - Invalid UTF-8, which a terminal would interpret in its own way.
//
// A line with nothing to neutralize is returned unchanged, with no allocation.
func SafeLine(s string) string {
	if isSafe(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b {
			if n := allowedSequence(s, i); n > 0 {
				b.WriteString(s[i : i+n])
				i += n
				continue
			}
			b.WriteString(escSymbol)
			i++
			continue
		}
		if c < 0x80 {
			switch {
			case c == '\t':
				b.WriteString(tabSpaces)
			case c >= 0x20 && c != 0x7f:
				b.WriteByte(c)
			case c == 0x07:
				b.WriteString(belSymbol)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++ // invalid UTF-8: dropped
			continue
		}
		if r >= 0x80 && r <= 0x9f {
			i += size // C1 control: dropped
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

const (
	escSymbol = "\u241b" // ␛ — what a neutralized ESC is shown as
	belSymbol = "\u2407" // ␇ — what a BEL is shown as
)

// maxURLBytes bounds a hyperlink target. A hyperlink's URL is invisible text handed to the
// terminal, so it is kept short and plain.
const maxURLBytes = 2048

// SafeURL reports whether url may be the target of an OSC 8 hyperlink: an http, https or mailto URL
// of bounded length with no control character, space or non-ASCII byte. A model can write any URL
// in a markdown link, and the terminal may open it on click, so only the web and mail schemes are
// allowed; anything else (file:, javascript:, a custom handler…) is rendered as plain text.
func SafeURL(url string) bool {
	if url == "" || len(url) > maxURLBytes {
		return false
	}
	for i := 0; i < len(url); i++ {
		if c := url[i]; c <= 0x20 || c >= 0x7f {
			return false
		}
	}
	l := strings.ToLower(url)
	return strings.HasPrefix(l, "http://") && len(l) > len("http://") ||
		strings.HasPrefix(l, "https://") && len(l) > len("https://") ||
		strings.HasPrefix(l, "mailto:") && len(l) > len("mailto:")
}

// isSafe is the fast path: true when SafeLine would return s untouched. It must agree with SafeLine
// exactly — a line it accepts is never modified, a line it rejects goes through the full pass.
func isSafe(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			n := allowedSequence(s, i)
			if n == 0 {
				return false
			}
			i += n
		case c < 0x80:
			if c < 0x20 || c == 0x7f { // TAB included: it is expanded
				return false
			}
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 || r >= 0x80 && r <= 0x9f {
				return false
			}
			i += size
		}
	}
	return true
}

// allowedSequence returns the length of the escape sequence starting at s[i] (an ESC) if it is one
// the TUI emits itself, and 0 otherwise.
func allowedSequence(s string, i int) int {
	if i+1 >= len(s) {
		return 0
	}
	switch s[i+1] {
	case '[':
		return allowedCSI(s, i)
	case ']':
		return allowedOSC8(s, i)
	}
	return 0
}

// allowedCSI accepts ESC [ <params> m when allowedSGR accepts the params, and the bare ESC [ K
// (erase to end of line). Anything else — a private-mode '?' or '<' '=' '>' prefix, an intermediate
// byte, any other final byte — is refused.
func allowedCSI(s string, i int) int {
	j := i + 2
	for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';' || s[j] == ':') {
		j++
	}
	if j >= len(s) {
		return 0
	}
	switch {
	case s[j] == 'm' && allowedSGR(s[i+2:j]):
		return j + 1 - i
	case s[j] == 'K' && j == i+2:
		return j + 1 - i
	}
	return 0
}

// maxSGRParams bounds the number of parameters of one SGR sequence; the TUI's own longest is a
// foreground and a background truecolor pair (10 numbers).
const maxSGRParams = 12

// allowedSGR reports whether the parameter string of an SGR sequence is made only of what the TUI
// uses: reset (empty or 0); bold, dim, italic, underline, inverse, strike (1 2 3 4 7 9) and their
// resets (22 23 24 27 29); the 16 standard colors (30–37, 40–47, 90–97, 100–107) and the default
// (39, 49); and extended colors 38/48 with ;5;N (256) or ;2;R;G;B (truecolor), every value 0–255.
//
// Not allowed: conceal (8), blink (5, 6), fast/other attributes, and any value or shape unknown. A
// color can still be chosen to match the background — that is styling, indistinguishable from a
// legitimate color, and not something a terminal interprets — but text can no longer be made
// invisible outright.
//
// It runs on every styled line of every frame, so it makes a single pass over the string and
// allocates nothing. An empty parameter (as in "1;;4") is a 0, which is how terminals read it.
func allowedSGR(params string) bool {
	if params == "" {
		return true // ESC [ m is a reset
	}
	// Walk the parameters one at a time. need counts the numbers still owed to an extended color
	// (38/48), and mode remembers which form it is, so a 38 cannot be followed by a stray value.
	count := 0
	need, mode := 0, 0 // mode: 0 none, 1 = just saw 38/48 (next is the form), 2 = 256 index, 3 = RGB
	i := 0
	for i <= len(params) {
		// read one parameter: digits up to the next separator (or the end)
		j := i
		n := 0
		for j < len(params) && params[j] != ';' && params[j] != ':' {
			if j-i >= 3 {
				return false // more than three digits: no valid value is that long
			}
			n = n*10 + int(params[j]-'0')
			j++
		}
		count++
		if count > maxSGRParams {
			return false
		}
		switch {
		case mode == 1: // the form of an extended color
			switch n {
			case 5:
				mode, need = 2, 1
			case 2:
				mode, need = 3, 3
			default:
				return false
			}
		case mode >= 2: // the values of an extended color
			if n > 255 {
				return false
			}
			if need--; need == 0 {
				mode = 0
			}
		case n == 38 || n == 48:
			mode = 1
		case n == 0 || n == 1 || n == 2 || n == 3 || n == 4 || n == 7 || n == 9,
			n == 22 || n == 23 || n == 24 || n == 27 || n == 29 || n == 39 || n == 49,
			n >= 30 && n <= 37, n >= 40 && n <= 47, n >= 90 && n <= 97, n >= 100 && n <= 107:
			// a plain attribute or standard color
		default:
			return false // 5, 6, 8, 21, 53… and anything else unknown
		}
		i = j + 1
	}
	return mode == 0 // an extended color left without its values is malformed
}

// allowedOSC8 accepts exactly the two forms Hyperlink emits, terminated by ST (ESC \):
//
//	ESC ] 8 ; ; <url> ESC \     with a url that passes SafeURL
//	ESC ] 8 ; ; ESC \           the closing one
//
// The parameters field must be empty: it is where a hyperlink would carry an id=, and nothing here
// uses one. BEL-terminated forms are refused (the TUI never emits them).
func allowedOSC8(s string, i int) int {
	const pre = "\x1b]8;;"
	if !strings.HasPrefix(s[i:], pre) {
		return 0
	}
	start := i + len(pre)
	end := strings.Index(s[start:], "\x1b\\")
	if end < 0 {
		return 0
	}
	url := s[start : start+end]
	if url != "" && !SafeURL(url) {
		return 0
	}
	return len(pre) + end + 2
}

// StripEscapes removes every escape sequence from s, and the control characters that would act on a
// terminal (BEL, BS, CR, NUL, DEL, C1…), for text that is processed rather than displayed as it
// comes: there, a sequence has no business surviving even neutralized. Line feed and TAB are kept,
// because they are layout the caller decides about (a one-line summary collapses them itself).
//
// Unlike ExtractAnsiCode — which recognizes only what the TUI emits and is meant for MEASURING —
// this knows the whole shape of an escape sequence: CSI with any parameter, intermediate and final
// byte; OSC, DCS, APC, PM and SOS strings up to BEL or ST; and the two-byte forms. A string
// sequence that is never terminated is not allowed to swallow the rest of the text: only its
// introducer is dropped and the text after it is kept.
func StripEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			if c < 0x80 {
				if c >= 0x20 && c != 0x7f || c == '\t' || c == '\n' {
					b.WriteByte(c)
				}
				i++
				continue
			}
			r, size := utf8.DecodeRuneInString(s[i:])
			if !(r == utf8.RuneError && size == 1) && !(r >= 0x80 && r <= 0x9f) {
				b.WriteString(s[i : i+size])
			}
			i += size
			continue
		}
		i += escapeLength(s, i)
	}
	return b.String()
}

// escapeLength is the length of the escape sequence at s[i] (an ESC), always at least 1.
func escapeLength(s string, i int) int {
	if i+1 >= len(s) {
		return 1
	}
	switch s[i+1] {
	case '[': // CSI: parameters 0x30–0x3F, intermediates 0x20–0x2F, one final byte 0x40–0x7E
		j := i + 2
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1 - i
		}
		return 2 // malformed: drop the introducer, keep what follows
	case ']', 'P', '_', '^', 'X': // string sequences: up to BEL or ST
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1 - i
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2 - i
			}
		}
		return 2 // unterminated: do not swallow the text
	}
	// Two-byte forms (ESC c, ESC 7, ESC D…) and charset designations (ESC ( 0).
	if s[i+1] >= 0x20 && s[i+1] <= 0x2f && i+2 < len(s) {
		return 3
	}
	return 2
}
