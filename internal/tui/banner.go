package tui

import (
	"math/rand"
	"strings"

	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/version"
)

// bannerArt is the "harness" wordmark (half-block font). Rendered in the accent
// color (chartreuse) — the primary teal is reserved for the user prompt.
var bannerArt = []string{
	"█ █ ▄▀█ █▀█ █▄ █ █▀▀ █▀ █▀",
	"█▀█ █▀█ █▀▄ █ ▀█ ██▄ ▄█ ▄█",
}

// bannerTips is the pool of one-line tips; one is shown at random on startup.
var bannerTips = []string{
	"Type a message, or / for commands.",
	"Use /model to switch models on the fly.",
	"/connect adds a provider; /disconnect removes one.",
	"Tab autocompletes commands and arguments.",
	"/resume picks up a previous session.",
	"Configure MCP servers with the 'harness mcp' command.",
	"/thinking sets the reasoning effort: off·low·medium·high·xhigh.",
	"Send a message mid-turn and it queues automatically.",
	"/compact summarizes the conversation to reclaim context.",
}

// welcomeBanner builds the startup banner: the wordmark, a tagline with the
// version and active model, a random tip, and any startup warnings (e.g. the
// configured model wasn't available, or no provider is active at all) —
// rendered as their own lines right below the tip, inside this SAME block.
// Shown only for a NEW session.
//
// The banner is harness's identity and is ALWAYS shown, even when startup hit
// a problem serious enough that no session gets created (no active providers,
// server unreachable, …) — warnings surface INSIDE it instead of the banner
// being skipped and a bare warning shown on its own above nothing. When no
// model could be resolved (t.model == ""), the tagline shows "-" in the
// model's place so the banner keeps the same shape either way.
func (t *TUI) welcomeBanner(warnings ...string) string {
	var b []byte
	add := func(s string) { b = append(b, s...); b = append(b, '\n') }

	add("")
	for _, line := range bannerArt {
		add("  " + ansi.Accent(line))
	}
	add("")
	tagline := "  " + ansi.Muted("fast terminal agent") + ansi.Dimmed(" · "+version.Version) + ansi.Dimmed(" · ")
	if t.model != "" {
		tagline += ansi.Muted(shortModel(t.model))
	} else {
		tagline += ansi.Muted("-")
	}
	add(tagline)
	add("")
	tip := bannerTips[rand.Intn(len(bannerTips))]
	// "⁘" (U+2058, FOUR DOT PUNCTUATION) marks the tip line — a deliberate
	// upgrade from a plain "·" bullet to something a little more distinct
	// while staying in the same monochrome-symbol family as every other TUI
	// icon (✔, ✘, ▶, ◎, ⚠).
	add("  " + ansi.Dimmed("⁘ ") + ansi.Muted(tip))
	if len(warnings) > 0 {
		// One blank line separates the tip from the warnings BLOCK as a
		// whole — the warnings themselves stack with no gap between them,
		// one compact block, not spaced apart from each other.
		add("")
		for _, w := range warnings {
			add("  " + ansi.Warn("⚠ "+w))
		}
	}
	// One blank line below the last thing in the banner so the editor
	// doesn't sit flush against it.
	add("")

	// `add` appends '\n' after EVERY line, so the final add("") above leaves
	// the string ending in "\n\n": one '\n' closing the last real line (the
	// tip, or the last warning if any were passed), one from add("") itself.
	// RawBlock.Render wraps this through ansi.WrapTextWithAnsi, which does
	// strings.Split(text, "\n") — where a trailing '\n' yields a final empty
	// element (Go semantics: "a\n".Split("\n") == ["a", ""]). Two trailing
	// newlines therefore became TWO blank lines, and combined with the idle
	// spinner's own blank line (components/spinner.go returns [""] while
	// stopped) the gap above the input separator was three blank lines
	// instead of the intended two.
	//
	// Trim exactly one: the remaining single '\n' still closes that last
	// line and still splits into the one blank line this banner is supposed
	// to leave. The blank lines built above are real content and are
	// untouched.
	return strings.TrimSuffix(string(b), "\n")
}
