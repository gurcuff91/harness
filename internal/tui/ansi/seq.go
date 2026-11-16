package ansi

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Escape sequences used by the differential renderer and terminal control.
// Mirrors the sequences emitted by PI's TUI and ProcessTerminal.
const (
	// Synchronized output (CSI 2026) — atomic, flicker-free frames.
	SyncBegin = "\x1b[?2026h"
	SyncEnd   = "\x1b[?2026l"

	// Cursor visibility.
	HideCursor = "\x1b[?25l"
	ShowCursor = "\x1b[?25h"

	// Clearing.
	ClearLine       = "\x1b[2K" // clear entire current line
	ClearToLineEnd  = "\x1b[K"  // clear from cursor to end of line
	ClearFromCursor = "\x1b[J"  // clear from cursor to end of screen
	ClearScreenHome = "\x1b[2J\x1b[H"
	ClearScrollback = "\x1b[3J"
	// FullClear clears screen, homes the cursor, then drops scrollback.
	FullClear = "\x1b[2J\x1b[H\x1b[3J"

	// Carriage return + newline used when emitting buffered frames.
	CRLF = "\r\n"
	CR   = "\r"

	// Bracketed paste mode toggles.
	BracketedPasteOn  = "\x1b[?2004h"
	BracketedPasteOff = "\x1b[?2004l"
	PasteStart        = "\x1b[200~"
	PasteEnd          = "\x1b[201~"

	// Mouse button-event tracking (SGR extended protocol) is intentionally
	// NOT enabled: the mode captures every mouse click+drag as an ANSI sequence,
	// which prevents the terminal from performing its native text selection —
	// users would no longer be able to click+drag to copy the agent's output.
	// The TUI sticks to the bottom while streaming; the terminal's native
	// scrollback is used to read earlier output after a turn completes.
)

// MoveUp returns the sequence to move the cursor up n rows (n>0).
func MoveUp(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dA", n)
}

// MoveDown returns the sequence to move the cursor down n rows (n>0).
func MoveDown(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dB", n)
}

// MoveTo returns the sequence to position the cursor at (row, col), 1-indexed.
func MoveTo(row, col int) string {
	return fmt.Sprintf("\x1b[%d;%dH", row, col)
}

// SetTitle returns the OSC 0 sequence that sets the terminal's title:
//
//	ESC ] 0 ; <title> BEL
//
// The title is written as given, so it must not contain a control character: BEL or ST would end
// the sequence early and anything after would be printed (or, with an ESC, interpreted) by the
// terminal. Callers that do not control the text pass it through SanitizeTitle first.
func SetTitle(title string) string {
	return "\x1b]0;" + title + "\x07"
}

// MaxTitleRunes is the longest title SanitizeTitle lets through.
const MaxTitleRunes = 80

// SanitizeTitle makes text safe to put in an OSC 0 title: control characters (C0, DEL and C1 —
// which include ESC, BEL, newline and the 8-bit forms of CSI/OSC/ST) are dropped, runs of
// whitespace collapse to one space, the ends are trimmed, and the result is cut to MaxTitleRunes
// runes (never in the middle of a multibyte character), with an ellipsis when it was cut.
//
// A session's name can come from the user's own prompt, so it is treated as hostile: an
// unsanitized BEL would end the sequence early and an ESC would let the name inject sequences of
// its own. Invalid UTF-8 is dropped too, rather than passed on for the terminal to guess at.
func SanitizeTitle(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			continue // invalid UTF-8 (or a literal U+FFFD, which says nothing either)
		case unicode.IsControl(r):
			// A line break or tab separates words; the other controls (ESC, BEL, NUL, C1…) are
			// simply dropped, which also defuses any escape sequence they would have started.
			if r == '\n' || r == '\r' || r == '\t' {
				space = true
			}
			continue
		case unicode.IsSpace(r):
			space = true
			continue
		}
		if space && n > 0 {
			b.WriteByte(' ')
			n++
		}
		space = false
		b.WriteRune(r)
		n++
	}
	out := b.String()
	if utf8.RuneCountInString(out) <= MaxTitleRunes {
		return out
	}
	rs := []rune(out)[:MaxTitleRunes-1]
	return strings.TrimRight(string(rs), " ") + "…"
}

// Hyperlink wraps text in an OSC 8 hyperlink escape so terminals that support
// it render text as a clickable link opening url. Terminals without support
// simply show text (the escapes are ignored). Uses the ST (ESC\) terminator.
//
//	ESC ] 8 ; ; <url> ESC \  <text>  ESC ] 8 ; ; ESC \
func Hyperlink(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// Program Status Protocol (OSC 7501, rev 0.4) states. A terminal that implements the
// protocol (Herdr, for one) shows them in its sidebar/tab indicators; a terminal that does not
// ignores the sequence, so nothing needs to be detected.
const (
	StatusWorking = "working" // a turn is running
	StatusIdle    = "idle"    // nothing is running; the last turn was stopped or the stream went away
	StatusDone    = "done"    // a turn finished normally
	StatusError   = "error"   // a turn (or a command) failed
	StatusClear   = "clear"   // drop this program's record — on exit
)

// ProgramStatus returns the OSC 7501 sequence reporting state for app:
//
//	ESC ] 7501 ; state=<state>:app=<app> ESC \
//
// The terminator is ST (ESC \), as the spec's examples use; BEL would also be valid. Each report
// replaces its record entirely, so app is part of EVERY report, never just the first.
func ProgramStatus(state, app string) string {
	return "\x1b]7501;state=" + state + ":app=" + app + "\x1b\\"
}
