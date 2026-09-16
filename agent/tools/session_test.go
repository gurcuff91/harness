package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
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

// ── SessionSearch ────────────────────────────────────────────────────────
//
// SessionSearch is now a thin adapter over an injected SessionSearchFunc —
// the actual FTS5 search logic lives in agent/store (FileStore.SearchMessages),
// tested there directly. These tests cover ONLY the tool's own
// responsibilities: input parsing/validation, limit clamping, delegating to
// the injected func, and translating store.ErrSearchNotSupported into the
// expected user-facing error.

func TestSessionSearchDelegatesQueryAndClampedLimit(t *testing.T) {
	var gotQuery string
	var gotLimit int
	fn := func(query string, limit int) ([]store.SearchResult, error) {
		gotQuery = query
		gotLimit = limit
		return []store.SearchResult{{Role: "user", Snippet: "a [match] here"}}, nil
	}
	tool := SessionSearch(fn)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"PKCE","limit":5}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotQuery != "PKCE" {
		t.Errorf("query = %q, want %q", gotQuery, "PKCE")
	}
	if gotLimit != 5 {
		t.Errorf("limit = %d, want 5", gotLimit)
	}
	var results []store.SearchResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Snippet != "a [match] here" {
		t.Errorf("unexpected results: %s", out)
	}
}

func TestSessionSearchLimitDefaultsAndClamps(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantLimit int
	}{
		{"zero uses default", `{"query":"x","limit":0}`, sessionSearchDefaultLimit},
		{"missing uses default", `{"query":"x"}`, sessionSearchDefaultLimit},
		{"negative uses default", `{"query":"x","limit":-5}`, sessionSearchDefaultLimit},
		{"over max clamps", `{"query":"x","limit":1000}`, sessionSearchMaxLimit},
		{"within range passes through", `{"query":"x","limit":3}`, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotLimit int
			fn := func(query string, limit int) ([]store.SearchResult, error) {
				gotLimit = limit
				return nil, nil
			}
			tool := SessionSearch(fn)
			if _, err := tool.Execute(context.Background(), json.RawMessage(c.input)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotLimit != c.wantLimit {
				t.Errorf("limit = %d, want %d", gotLimit, c.wantLimit)
			}
		})
	}
}

func TestSessionSearchRejectsEmptyQuery(t *testing.T) {
	fn := func(query string, limit int) ([]store.SearchResult, error) {
		t.Fatal("search func must not be called for an empty query")
		return nil, nil
	}
	tool := SessionSearch(fn)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":""}`))
	if err == nil {
		t.Fatal("expected an error for empty query")
	}
}

func TestSessionSearchTranslatesErrSearchNotSupported(t *testing.T) {
	fn := func(query string, limit int) ([]store.SearchResult, error) {
		return nil, store.ErrSearchNotSupported
	}
	tool := SessionSearch(fn)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"anything"}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "does not support search") {
		t.Errorf("expected a plain 'does not support search' message, got: %v", err)
	}
}

func TestSessionSearchNilResultsBecomeEmptyArray(t *testing.T) {
	fn := func(query string, limit int) ([]store.SearchResult, error) {
		return nil, nil
	}
	tool := SessionSearch(fn)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"anything"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
}

func TestSessionSearchPropagatesOtherErrors(t *testing.T) {
	fn := func(query string, limit int) ([]store.SearchResult, error) {
		return nil, context.DeadlineExceeded
	}
	tool := SessionSearch(fn)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"anything"}`))
	if err == nil {
		t.Fatal("expected an error")
	}
}
