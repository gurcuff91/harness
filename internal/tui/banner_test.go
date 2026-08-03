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

// TestWelcomeBannerShowsModelPlaceholderWhenEmpty verifies the banner's
// identity is shown even when no model was resolved (server unreachable, no
// active providers, …): the tagline shows "-" in the model's place instead
// of silently omitting that segment, so the banner keeps the same shape
// whether or not startup fully succeeded.
func TestWelcomeBannerShowsModelPlaceholderWhenEmpty(t *testing.T) {
	tui := &TUI{model: ""}
	banner := stripANSI(tui.welcomeBanner())
	if !strings.Contains(banner, "· -") {
		t.Errorf("banner with no model should show '· -' in the tagline, got:\n%s", banner)
	}
}

// TestWelcomeBannerRendersWarningsInsideTheSameBlock is the regression test
// for the reported issue: a startup warning (e.g. the configured model isn't
// available) used to print as its own bare notice ABOVE the banner — this
// verifies it instead renders INSIDE welcomeBanner's own returned string,
// on its own line, indented to match the tip line above it.
func TestWelcomeBannerRendersWarningsInsideTheSameBlock(t *testing.T) {
	tui := &TUI{model: "anthropic/claude-test"}
	banner := stripANSI(tui.welcomeBanner("Model 'x/y' not available. Using first active model."))

	if !strings.Contains(banner, "⚠ Model 'x/y' not available. Using first active model.") {
		t.Errorf("banner should contain the warning text, got:\n%s", banner)
	}
	// Indented the same as the tip line ("  ⁘ ...").
	if !strings.Contains(banner, "  ⚠ Model 'x/y'") {
		t.Errorf("warning should be indented with the same 2-space prefix as the tip line, got:\n%s", banner)
	}
}

// TestWelcomeBannerStacksMultipleWarningsWithNoGapBetweenThem verifies
// several warnings render as ONE compact block — one blank line separates
// the tip from the whole warnings block, but consecutive warnings stack
// with no blank line between each other.
func TestWelcomeBannerStacksMultipleWarningsWithNoGapBetweenThem(t *testing.T) {
	tui := &TUI{model: ""}
	banner := tui.welcomeBanner(
		"No active providers. Use /connect to add one.",
		"Failed to resume: session abc123 not found.",
	)

	lines := strings.Split(banner, "\n")
	var warnIdx []int
	for i, l := range lines {
		if strings.Contains(l, "⚠") {
			warnIdx = append(warnIdx, i)
		}
	}
	if len(warnIdx) != 2 {
		t.Fatalf("expected 2 warning lines, found %d: %v", len(warnIdx), lines)
	}
	if warnIdx[1] != warnIdx[0]+1 {
		t.Errorf("warnings should be on CONSECUTIVE lines (no blank line between them), got lines %d and %d:\n%s",
			warnIdx[0], warnIdx[1], banner)
	}
	// Exactly one blank line must separate the tip from the warnings block.
	if lines[warnIdx[0]-1] != "" {
		t.Errorf("expected exactly one blank line right before the warnings block, got %q at line %d:\n%s",
			lines[warnIdx[0]-1], warnIdx[0]-1, banner)
	}
	if lines[warnIdx[0]-2] == "" {
		t.Errorf("expected only ONE blank line before the warnings block (not two), got another blank at line %d:\n%s",
			warnIdx[0]-2, banner)
	}
}

// TestWelcomeBannerNoWarningsOmitsTheGapEntirely verifies the zero-warnings
// case (the common one) doesn't grow an extra blank line for a warnings
// block that isn't there — same shape as before warnings existed at all.
func TestWelcomeBannerNoWarningsOmitsTheGapEntirely(t *testing.T) {
	tui := &TUI{model: "anthropic/claude-test"}
	banner := tui.welcomeBanner()
	if strings.Contains(banner, "⚠") {
		t.Errorf("banner with no warnings should contain no ⚠ markers, got:\n%s", banner)
	}
}

// TestWelcomeBannerTipUsesFourDotIcon verifies the tip line uses "⁘" (U+2058
// FOUR DOT PUNCTUATION) instead of a plain "·" bullet — a deliberate visual
// upgrade requested to make the tip line more distinct, still monochrome
// like every other TUI icon.
func TestWelcomeBannerTipUsesFourDotIcon(t *testing.T) {
	tui := &TUI{model: "anthropic/claude-test"}
	banner := stripANSI(tui.welcomeBanner())
	if !strings.Contains(banner, "  ⁘ ") {
		t.Errorf("tip line should be marked with '⁘ ', got:\n%s", banner)
	}
}
