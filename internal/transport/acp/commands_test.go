package acp

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
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
	text := formatInfoPlain(info)
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
	if text := formatInfoPlain(nil); !strings.Contains(text, "No session info") {
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
