package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// ── SessionInfo ──────────────────────────────────────────────────────────

func TestSessionInfoReturnsExactFieldSet(t *testing.T) {
	tool := SessionInfo(func() SessionInfoSnapshot {
		return SessionInfoSnapshot{
			ID: "sess-1", CWD: "/proj", Name: "my session",
			Model: "anthropic/claude", Thinking: "high", CreatedAt: "2026-01-01T00:00:00Z",
			Version: "v0.76.75", MCPConnected: 2, ScheduleCount: 1,
			InputTokens: 100, OutputTokens: 50, CacheRead: 10, CacheWrite: 5,
			CostUSD: 0.01, ContextUsage: 0.1, ContextWindow: 128000,
		}
	})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}

	wantKeys := []string{
		"id", "cwd", "name", "model", "thinking", "created_at",
		"version", "mcp_connected", "schedule_count", "os", "arch", "channels",
		"input_tokens", "output_tokens", "cache_read", "cache_write",
		"cost_usd", "context_usage", "context_window",
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing expected key %q in output: %s", k, out)
		}
	}
	// Explicitly excluded fields must be genuinely absent, not just zero-valued.
	excludedKeys := []string{"compact_count", "last_active_at", "stats", "compact_offset", "max_iterations"}
	for _, k := range excludedKeys {
		if _, ok := got[k]; ok {
			t.Errorf("output must NOT contain %q (internal/compaction bookkeeping): %s", k, out)
		}
	}
	if len(got) != len(wantKeys) {
		t.Errorf("got %d keys, want exactly %d: %s", len(got), len(wantKeys), out)
	}
}

// The environment block: terminal only when known, channels always an array.
func TestSessionInfoEnvironment(t *testing.T) {
	run := func(s SessionInfoSnapshot) map[string]any {
		out, err := SessionInfo(func() SessionInfoSnapshot { return s }).Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		_ = json.Unmarshal([]byte(out), &got)
		return got
	}
	got := run(SessionInfoSnapshot{OS: "darwin", Arch: "arm64", Terminal: "Workspace (t16)",
		Channels: []SessionChannelInfo{{Type: "slack", ID: "C1", Name: "#prod"}}})
	if got["os"] != "darwin" || got["arch"] != "arm64" || got["terminal"] != "Workspace (t16)" {
		t.Errorf("environment = %v", got)
	}
	ch, _ := got["channels"].([]any)
	if len(ch) != 1 || ch[0].(map[string]any)["name"] != "#prod" {
		t.Errorf("channels = %v", got["channels"])
	}
	none := run(SessionInfoSnapshot{Channels: []SessionChannelInfo{}})
	if _, ok := none["terminal"]; ok {
		t.Error("terminal present without one")
	}
	if c, ok := none["channels"].([]any); !ok || len(c) != 0 {
		t.Errorf("channels must be an empty array, got %v", none["channels"])
	}
}
