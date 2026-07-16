package ansi

import (
	"fmt"
	"strconv"
)

// ── Kaiban palette (single source of truth, mirrors transport/tui) ──────────
const (
	HexPrimary = "#26A69A" // teal 400   — Kaiban Teal (dark-adapted)
	HexAccent  = "#C8D96A" // chartreuse — Kaiban Energy
	HexErr     = "#D94068" // rose       — Kaiban Rose
	HexWarn    = "#B44CA0" // violet     — Kaiban Violet
)

// SGR control codes.
const (
	Reset  = "\x1b[0m"
	Bold   = "\x1b[1m"
	Dim    = "\x1b[2m"
	Ital   = "\x1b[3m"
	Under  = "\x1b[4m"
	Inv    = "\x1b[7m"
	Strike = "\x1b[9m"
)

// HexMuted is a light gray for secondary text that must still read clearly —
// paired with Bold in Muted() so tool result/error summaries stand apart from
// the fainter Dimmed args without competing with the bright tool name.
const HexMuted = "#AEB6BF"

// Pre-built foreground openers for the palette (truecolor SGR).
var (
	fgPrimary = hexFG(HexPrimary)
	fgAccent  = hexFG(HexAccent)
	fgErr     = hexFG(HexErr)
	fgWarn    = hexFG(HexWarn)
	fgMuted   = hexFG(HexMuted)
)

// hexFG returns the truecolor foreground SGR opener for a #rrggbb hex string.
func hexFG(hex string) string {
	r, g, b := hexRGB(hex)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// hexBG returns the truecolor background SGR opener for a #rrggbb hex string.
func hexBG(hex string) string {
	r, g, b := hexRGB(hex)
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

func hexRGB(hex string) (int, int, int) {
	if len(hex) > 0 && hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) != 6 {
		return 255, 255, 255
	}
	r, _ := strconv.ParseInt(hex[0:2], 16, 0)
	g, _ := strconv.ParseInt(hex[2:4], 16, 0)
	b, _ := strconv.ParseInt(hex[4:6], 16, 0)
	return int(r), int(g), int(b)
}

// ── Style helpers — wrap text in an SGR span, auto-resetting ────────────────

// FG wraps s in a truecolor foreground span.
func FG(hex, s string) string { return hexFG(hex) + s + Reset }

// Primary, Accent, Err, Warn wrap s in the corresponding palette color.
func Primary(s string) string { return fgPrimary + s + Reset }
func Accent(s string) string  { return fgAccent + s + Reset }
func Err(s string) string     { return fgErr + s + Reset }
func Warn(s string) string    { return fgWarn + s + Reset }

// Dimmed wraps s in the dim (faint) attribute.
func Dimmed(s string) string { return Dim + s + Reset }

// Muted wraps s in BOLD + a light gray foreground. Bold gives the text weight
// ("gordita") so it reads clearly, while the gray keeps it secondary to the
// tool name. Used for tool result/error summaries so they stand out from the
// fainter Dimmed args. (Dim+Bold is contradictory and unreliable across
// terminals, so we use Bold + a brighter gray instead of the faint attribute.)
func Muted(s string) string { return Bold + fgMuted + s + Reset }

// Boldify wraps s in bold.
func Boldify(s string) string { return Bold + s + Reset }

// PrimaryBold combines bold + primary color.
func PrimaryBold(s string) string { return Bold + fgPrimary + s + Reset }

// Cursor renders a fake block cursor: the character under the cursor on a
// primary-colored background with dark foreground. Matches the v1 TUI's
// emerald block cursor.
func Cursor(s string) string {
	return hexBG(HexPrimary) + "\x1b[38;2;26;26;26m" + s + Reset
}
