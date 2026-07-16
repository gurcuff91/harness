// Package keys provides terminal key detection for tui.
//
// It maps raw input sequences (as delivered by term.stdinBuffer) to symbolic
// key names. This is a pragmatic subset of PI's keys.ts covering the keys the
// harness UI needs: navigation, editing, and common control combos under the
// standard VT100 / xterm encoding.
//
// NOTE: Kitty keyboard protocol disambiguation (Shift+Enter, Ctrl+Enter, etc.)
// is intentionally not handled here yet — see the TODO in term/terminal.go.
package keys

// Key is a symbolic key identifier.
type Key string

const (
	Enter     Key = "enter"
	Escape    Key = "escape"
	Tab       Key = "tab"
	ShiftTab  Key = "shift+tab"
	Backspace Key = "backspace"
	Delete    Key = "delete"
	Up        Key = "up"
	Down      Key = "down"
	Left      Key = "left"
	Right     Key = "right"
	Home      Key = "home"
	End       Key = "end"
	Space     Key = "space"

	CtrlA Key = "ctrl+a"
	CtrlC Key = "ctrl+c"
	CtrlD Key = "ctrl+d"
	CtrlE Key = "ctrl+e"
	CtrlK Key = "ctrl+k"
	CtrlU Key = "ctrl+u"
	CtrlV Key = "ctrl+v"
	CtrlW Key = "ctrl+w"
	CtrlY Key = "ctrl+y"

	AltEnter     Key = "alt+enter"
	AltBackspace Key = "alt+backspace"
	CtrlLeft     Key = "ctrl+left"
	CtrlRight    Key = "ctrl+right"
	AltLeft      Key = "alt+left"
	AltRight     Key = "alt+right"
)

// seqMap maps fixed escape/control sequences to keys.
var seqMap = map[string]Key{
	"\r":     Enter,
	"\n":     Enter,
	"\x1b":   Escape,
	"\t":     Tab,
	"\x1b[Z": ShiftTab,
	"\x7f":   Backspace,
	"\x08":   Backspace,
	" ":      Space,

	"\x1b[A": Up,
	"\x1b[B": Down,
	"\x1b[C": Right,
	"\x1b[D": Left,
	"\x1bOA": Up,
	"\x1bOB": Down,
	"\x1bOC": Right,
	"\x1bOD": Left,

	"\x1b[H":  Home,
	"\x1b[F":  End,
	"\x1b[1~": Home,
	"\x1b[4~": End,
	"\x1b[3~": Delete,
	"\x1b[7~": Home,
	"\x1b[8~": End,

	// Word navigation (xterm modifier-aware sequences).
	"\x1b[1;5D": CtrlLeft,
	"\x1b[1;5C": CtrlRight,
	"\x1b[1;3D": AltLeft,
	"\x1b[1;3C": AltRight,
	"\x1bb":     AltLeft,  // Alt+b
	"\x1bf":     AltRight, // Alt+f

	// Alt+Enter (reliable newline; see term TODO).
	"\x1b\r": AltEnter,
	"\x1b\n": AltEnter,

	// Alt/Ctrl backspace variants.
	"\x1b\x7f": AltBackspace,
	"\x1b\x08": AltBackspace,
	"\x17":     CtrlW,
}

// ctrlMap maps control bytes (0x01-0x1A) to ctrl+<letter> keys, for the ones
// not already claimed by fixed sequences (Enter, Tab, Backspace).
var ctrlMap = map[byte]Key{
	0x01: CtrlA,
	0x03: CtrlC,
	0x04: CtrlD,
	0x05: CtrlE,
	0x0b: CtrlK,
	0x15: CtrlU,
	0x16: CtrlV,
	0x17: CtrlW,
	0x19: CtrlY,
}

// Match reports whether the input sequence corresponds to key.
func Match(data string, key Key) bool {
	if k, ok := seqMap[data]; ok {
		return k == key
	}
	if len(data) == 1 {
		if k, ok := ctrlMap[data[0]]; ok {
			return k == key
		}
	}
	return false
}

// Lookup returns the symbolic key for an input sequence and whether it was
// recognized.
func Lookup(data string) (Key, bool) {
	if k, ok := seqMap[data]; ok {
		return k, true
	}
	if len(data) == 1 {
		if k, ok := ctrlMap[data[0]]; ok {
			return k, true
		}
	}
	return "", false
}

// IsPrintable reports whether data is a single printable character (not a
// control sequence) suitable for insertion into a text buffer.
func IsPrintable(data string) bool {
	if data == "" {
		return false
	}
	// Reject anything starting with ESC (escape sequence).
	if data[0] == 0x1b {
		return false
	}
	// Reject single control bytes.
	if len(data) == 1 && data[0] < 0x20 {
		return false
	}
	if len(data) == 1 && data[0] == 0x7f {
		return false
	}
	return true
}
