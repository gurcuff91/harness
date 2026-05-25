package cli

import (
	"fmt"
	"os"
	"strings"
)

// ANSI color codes — pure terminal, zero dependencies.
const (
	Reset     = "\033[0m"
	Bold      = "\033[1m"
	Dim       = "\033[2m"
	Italic    = "\033[3m"
	Underline = "\033[4m"

	// Foreground
	Black   = "\033[30m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
	Gray    = "\033[90m"

	// Bright foreground
	BrightRed     = "\033[91m"
	BrightGreen   = "\033[92m"
	BrightYellow  = "\033[93m"
	BrightBlue    = "\033[94m"
	BrightMagenta = "\033[95m"
	BrightCyan    = "\033[96m"
	BrightWhite   = "\033[97m"

	// Background
	BgBlack   = "\033[40m"
	BgRed     = "\033[41m"
	BgGreen   = "\033[42m"
	BgYellow  = "\033[43m"
	BgBlue    = "\033[44m"
	BgMagenta = "\033[45m"
	BgCyan    = "\033[46m"
	BgWhite   = "\033[47m"
)

// NoColor disables all ANSI colors (for pipes/non-TTY).
var NoColor = !isTerminal()

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// C applies color codes if terminal supports it.
func C(color, text string) string {
	if NoColor {
		return text
	}
	return color + text + Reset
}

// Spinner characters for animation.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ClearLine erases the current terminal line.
func ClearLine() {
	if !NoColor {
		fmt.Fprint(out, "\033[2K\r")
		out.Flush()
	}
}

// Truncate shortens text to maxLen with ellipsis.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// OneLiner collapses multiline text to a single line.
func OneLiner(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.Join(strings.Fields(s), " ") // collapse whitespace
	return Truncate(s, maxLen)
}

// Box draws a bordered box around text.
func Box(title string, width int) string {
	if width <= 0 {
		width = 50
	}
	top := "╭" + strings.Repeat("─", width-2) + "╮"
	bot := "╰" + strings.Repeat("─", width-2) + "╯"

	// Center the title
	padding := width - 4 - len(title)
	if padding < 0 {
		padding = 0
	}
	left := padding / 2
	right := padding - left
	mid := "│ " + strings.Repeat(" ", left) + title + strings.Repeat(" ", right) + " │"

	return C(Cyan, top) + "\n" + C(Cyan, mid) + "\n" + C(Cyan, bot)
}

// Indent adds prefix to each line.
func Indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// FormatTokens formats token counts with color.
func FormatTokens(input, output int) string {
	return C(Dim, compactNum(input)+" in") + " / " + C(Dim, compactNum(output)+" out")
}

// compactNum formats a number compactly: 829, 1.2k, 8.0M
func compactNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000.0)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// joinParts joins non-empty strings with a separator.
func joinParts(parts []string, sep string) string {
	return strings.Join(parts, sep)
}

// formatCost formats a dollar amount compactly.
func formatCost(cost float64) string {
	if cost >= 1.0 {
		return fmt.Sprintf("$%.2f", cost)
	}
	if cost >= 0.01 {
		return fmt.Sprintf("$%.3f", cost)
	}
	return fmt.Sprintf("$%.4f", cost)
}
