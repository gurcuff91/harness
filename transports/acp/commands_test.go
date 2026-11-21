package acp

import (
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// TestCommandsExcludedFromACPMatchesExpectedSet locks the exclusion list to
// exactly the 4 known IDs — 2 covered by native configOptions (model,
// thinking) and 2 with no ACP equivalent at all (rename, reset) — so it
// can't silently grow or shrink. "compact" and any "skill:<name>" MUST NOT
// be in here: they're the only two commands executableCommand (methods.go)
// actually executes, and hiding them from available_commands_update would
// make them undiscoverable in Zed's UI despite fully working if typed.
func TestCommandsExcludedFromACPMatchesExpectedSet(t *testing.T) {
	want := map[string]bool{"model": true, "thinking": true, "rename": true, "reset": true}
	for id := range want {
		if !commandsExcludedFromACP[id] {
			t.Errorf("%q should be excluded from available_commands_update but isn't", id)
		}
	}
	for id := range commandsExcludedFromACP {
		if !want[id] {
			t.Errorf("unexpected id %q excluded from available_commands_update", id)
		}
	}
	if len(commandsExcludedFromACP) != len(want) {
		t.Errorf("commandsExcludedFromACP has %d entries, want %d: %v", len(commandsExcludedFromACP), len(want), commandsExcludedFromACP)
	}
}

// TestFormatInfoPlainRendersKeyFields verifies the /info formatter produces
// output containing the essential labels, wrapped in a fenced code block
// (see formatInfoPlain's doc comment for why the fence is mandatory — Zed
// renders agent_message_chunk text as Markdown in a proportional font,
// where fixed-width label/value padding only lines up inside a code fence).
func TestFormatInfoPlainRendersKeyFields(t *testing.T) {
	info := &client.SessionInfo{
		Version: "v0.74.9",
		Session: client.Session{
			SessionMeta: store.SessionMeta{
				ID:       "abc-123-def",
				Name:     "Test Session",
				Model:    "claude-oauth/claude-sonnet-5",
				Thinking: "medium",
			},
			MaxIterations: 120,
		},
		MCPConnected: 2,
	}
	text := formatInfoPlain(info, nil, nil, time.Now())
	for _, want := range []string{"harness", "session", "model", "thinking", "iters", "mcps"} {
		if !strings.Contains(text, want) {
			t.Errorf("formatInfoPlain: missing %q in output:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, "```\n") || !strings.HasSuffix(text, "```\n") {
		t.Errorf("formatInfoPlain: must be wrapped in a fenced code block for monospace alignment in Zed, got:\n%s", text)
	}
}

func TestFormatInfoPlainNil(t *testing.T) {
	if text := formatInfoPlain(nil, nil, nil, time.Now()); !strings.Contains(text, "No session info") {
		t.Errorf("formatInfoPlain(nil) = %q, want a warning message", text)
	}
}

// TestFormatContextPlainRendersKeyFields verifies the /context formatter
// produces output with the context breakdown components, wrapped in a
// fenced code block (same reasoning as TestFormatInfoPlainRendersKeyFields).
func TestFormatContextPlainRendersKeyFields(t *testing.T) {
	bd := &client.ContextBreakdown{
		System:         5000,
		Tools:          3000,
		Conversation:   2000,
		EstimatedTotal: 10000,
		LastRealTotal:  12000,
		ContextWindow:  200000,
		FreeSpace:      188000,
	}
	text := formatContextPlain(bd)
	for _, want := range []string{"system prompt", "tools", "conversation", "estimated", "actual", "free"} {
		if !strings.Contains(text, want) {
			t.Errorf("formatContextPlain: missing %q in output:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, "```\n") || !strings.HasSuffix(text, "```\n") {
		t.Errorf("formatContextPlain: must be wrapped in a fenced code block for monospace alignment in Zed, got:\n%s", text)
	}
}

func TestFormatContextPlainNil(t *testing.T) {
	if text := formatContextPlain(nil); !strings.Contains(text, "No context data") {
		t.Errorf("formatContextPlain(nil) = %q, want a warning message", text)
	}
}

// TestFenceTrimsTrailingNewlineBeforeClosing verifies fence() doesn't leave
// a blank line before the closing "```" when the input already ends in \n
// (the common case — every row-building helper in formatInfoPlain/
// formatContextPlain ends its last line with \n).
func TestFenceTrimsTrailingNewlineBeforeClosing(t *testing.T) {
	got := fence("a\nb\n")
	want := "```\na\nb\n```\n"
	if got != want {
		t.Errorf("fence(%q) = %q, want %q", "a\nb\n", got, want)
	}
}

// /info over ACP shows what the TUI's shows, minus the terminal: the id, the
// system, the quota of the session's provider (with words instead of colors
// for a window near its limit), the schedules and the channels.
func TestFormatInfoPlainMatchesTheTUIPanel(t *testing.T) {
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	info := &client.SessionInfo{
		Version: "v0.113.1", OS: "darwin", Arch: "arm64", Terminal: "Herdr (t3)",
		Session: client.Session{
			SessionMeta: store.SessionMeta{ID: "abc-123-def", Name: "Parser", Model: "claude-oauth/claude-opus-4-8"},
			Channels:    []client.ChannelInfo{{Type: "slack", ID: "C1", Name: "#prod"}, {Type: "slack", ID: "D2", Name: "@gus"}},
		},
		MCPConnected: 1,
	}
	q := &types.Quota{Provider: "claude-oauth", Plan: "max", FetchedAt: now, Windows: []types.QuotaWindow{
		{Name: "session", Label: "5-hour session", UsedPercent: 23, ResetsAt: now.Add(4*time.Hour + 5*time.Minute)},
		{Name: "weekly", Label: "Weekly (all models)", UsedPercent: 75, ResetsAt: now.Add(50 * time.Hour)},
		{Name: "weekly:Fable", Label: "Weekly (Fable)", Scope: "Fable", UsedPercent: 95},
	}}
	text := formatInfoPlain(info, q, nil, now)
	for _, want := range []string{
		"id              abc-123-def\n",
		"quota           claude-oauth · max\n",
		"session         ██░░░░░░░░  23%   resets in 4h 05m\n",
		"weekly          ████████░░  75%   resets in 2d 02h  ⚠ high\n",
		"Fable           ██████████  95%  ⚠ almost out\n",
		"system          darwin/arm64\n",
		"schedules       0\n",
		"slack           #prod, @gus\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "terminal") || strings.Contains(text, "Herdr") {
		t.Errorf("ACP has no terminal row:\n%s", text)
	}
	if strings.Contains(text, "\x1b") {
		t.Errorf("no ANSI inside the Markdown fence:\n%q", text)
	}
	// A provider without quota: no section. Any other failure: one row.
	if text := formatInfoPlain(info, nil, &client.Error{Status: 404, Message: "none"}, now); strings.Contains(text, "quota") {
		t.Errorf("a 404 shows a quota section:\n%s", text)
	}
	text = formatInfoPlain(info, nil, &client.Error{Status: 502, Message: "minimax usage endpoint: HTTP 500"}, now)
	if !strings.Contains(text, "quota           unavailable (minimax usage endpoint: HTTP 500)\n") {
		t.Errorf("a failure must be one unavailable row:\n%s", text)
	}
	// Ollama-style balance and a stale reading.
	text = formatInfoPlain(info, &types.Quota{Provider: "ollama-cloud", Stale: true, FetchedAt: now.Add(-2 * time.Minute), Windows: []types.QuotaWindow{},
		Balance: &types.QuotaBalance{RemainingUSD: 3, AllowanceUSD: 20, ResetsAt: now.Add(27 * time.Hour)}}, nil, now)
	for _, want := range []string{"· stale (2m ago)\n", "balance         $3.00 of $20.00   resets in 1d 03h\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}
