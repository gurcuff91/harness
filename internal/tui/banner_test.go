package tui

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// TestWelcomeBannerEndsWithExactlyOneBlankLine reproduces a rendering bug: the
// banner rendered with one blank line too many below it.
//
// welcomeBanner builds its text with an `add` helper that appends '\n' after
// EVERY line. The final add("") — intended to leave "one blank line below the
// tip so the editor doesn't sit flush against it" — therefore left the string
// ending in "\n\n": one '\n' closing the tip's line, one from add("") itself.
// RawBlock.Render wraps that text through ansi.WrapTextWithAnsi, which does
// strings.Split(text, "\n"); a trailing '\n' yields a final empty element
// (Go semantics: "a\n".Split("\n") == ["a", ""]). With TWO trailing newlines
// that became two empty elements — two blank lines. Combined with the
// spinner's own blank line while idle (components.Spinner.Render returns [""]
// when stopped), the gap above the input separator was THREE blank lines
// instead of the intended two (one from the banner, one from the idle
// spinner) — the visual jump reported below the banner/footer.
//
// The fix trims exactly one trailing '\n', so the banner ends in a single
// newline → a single final empty element → the one intended blank line.
func TestWelcomeBannerEndsWithExactlyOneBlankLine(t *testing.T) {
	tui := &TUI{model: "anthropic/claude-test"}
	banner := tui.welcomeBanner()

	// Mirrors exactly what RawBlock.Render does in production (history.go):
	// the banner is wrapped through WrapTextWithAnsi at render time.
	lines := ansi.WrapTextWithAnsi(banner, 80)

	if len(lines) == 0 {
		t.Fatal("banner rendered zero lines")
	}
	last := lines[len(lines)-1]
	if last != "" {
		t.Fatalf("banner's last rendered line should be blank, got %q", last)
	}
	if len(lines) >= 2 && lines[len(lines)-2] == "" {
		t.Errorf("banner ends with TWO consecutive blank lines (want exactly one): last lines = %q, %q",
			lines[len(lines)-2], lines[len(lines)-1])
	}

	// Pin the root cause at the source: the string must not end in the DOUBLE
	// newline it used to ("...tip\n" from add(tip), plus "\n" from the final
	// add("")). One trailing '\n' is correct and required — it closes the last
	// line, and strings.Split turning it into a final empty element is exactly
	// the one intended blank line. Two trailing newlines produced two.
	if strings.HasSuffix(banner, "\n\n") {
		t.Errorf("welcomeBanner() ends in a double newline — that's the extra blank line; tail=%q",
			banner[max(0, len(banner)-30):])
	}
}
