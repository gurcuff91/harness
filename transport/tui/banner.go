package tui

import (
	"math/rand"

	"github.com/gurcuff91/harness/transport/tui/ansi"
	"github.com/gurcuff91/harness/version"
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
// version and active model, and a random tip. Shown only for a NEW session.
func (t *TUI) welcomeBanner() string {
	var b []byte
	add := func(s string) { b = append(b, s...); b = append(b, '\n') }

	add("")
	for _, line := range bannerArt {
		add("  " + ansi.Accent(line))
	}
	add("")
	tagline := "  " + ansi.Muted("fast terminal agent") + ansi.Dimmed(" · v"+version.Version)
	if t.model != "" {
		tagline += ansi.Dimmed(" · ") + ansi.Muted(shortModel(t.model))
	}
	add(tagline)
	add("")
	tip := bannerTips[rand.Intn(len(bannerTips))]
	add("  " + ansi.Dimmed("· ") + ansi.Muted(tip))
	// One blank line below the tip so the editor doesn't sit flush against it.
	add("")
	return string(b)
}
