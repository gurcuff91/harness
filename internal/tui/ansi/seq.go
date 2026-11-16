package ansi

import "fmt"

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

// SetTitle returns the OSC sequence to set the terminal window title.
func SetTitle(title string) string {
	return fmt.Sprintf("\x1b]0;%s\x07", title)
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
