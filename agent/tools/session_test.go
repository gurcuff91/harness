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
		"version", "mcp_connected", "schedule_count",
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
