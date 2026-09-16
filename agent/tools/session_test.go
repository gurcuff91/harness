package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// ── SessionInfo ──────────────────────────────────────────────────────────

func TestSessionInfoReturnsExactFieldSet(t *testing.T) {
	tool := SessionInfo(func() SessionInfoSnapshot {
		return SessionInfoSnapshot{
			ID: "sess-1", CWD: "/proj", Name: "my session",
			Model: "anthropic/claude", Thinking: "high", CreatedAt: "2026-01-01T00:00:00Z",
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

	wantKeys := []string{"id", "cwd", "name", "model", "thinking", "created_at"}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing expected key %q in output: %s", k, out)
		}
	}
	// Explicitly excluded fields must be genuinely absent, not just zero-valued.
	excludedKeys := []string{"compact_count", "last_active_at", "stats", "compact_offset"}
	for _, k := range excludedKeys {
		if _, ok := got[k]; ok {
			t.Errorf("output must NOT contain %q (cost/compaction internals): %s", k, out)
		}
	}
	if len(got) != len(wantKeys) {
		t.Errorf("got %d keys, want exactly %d: %s", len(got), len(wantKeys), out)
	}
}

// ── SessionSearch ──────────────────────────────────────────────────────────

func msgText(role types.MessageRole, text string) types.Message {
	return types.Message{Role: role, Parts: []types.ContentPart{{Text: text}}}
}

func msgToolCall(id, name string, argsJSON string) types.Message {
	return types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{
		{ToolCall: &types.ToolCall{ID: id, Name: name, Input: json.RawMessage(argsJSON)}},
	}}
}

func msgToolResult(id, output string) types.Message {
	return types.Message{Role: types.RoleUser, Parts: []types.ContentPart{
		{ToolResult: &types.ToolResult{ID: id, Output: output}},
	}}
}

func tempIndexResolver(t *testing.T) SessionSearchIndexPathResolver {
	t.Helper()
	dir := t.TempDir()
	return func() string { return filepath.Join(dir, "session.search.db") }
}

func TestSessionSearchFindsIndexedText(t *testing.T) {
	all := []types.Message{
		msgText(types.RoleUser, "how does PKCE verification work"),
		msgText(types.RoleAssistant, "PKCE uses a code verifier and challenge to prevent interception"),
	}
	tool := SessionSearch(func() []types.Message { return all }, tempIndexResolver(t))

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"PKCE"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var results []sessionSearchResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (both messages mention PKCE): %s", len(results), out)
	}
}

func TestSessionSearchExcludesToolCallAndResult(t *testing.T) {
	all := []types.Message{
		msgToolCall("t1", "Bash", `{"command":"echo uniquemarker123"}`),
		msgToolResult("t1", "output containing uniquemarker123 too"),
		msgText(types.RoleUser, "unrelated text"),
	}
	tool := SessionSearch(func() []types.Message { return all }, tempIndexResolver(t))

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"uniquemarker123"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var results []sessionSearchResult
	_ = json.Unmarshal([]byte(out), &results)
	if len(results) != 0 {
		t.Errorf("tool_call/tool_result content must never be indexed, got: %s", out)
	}
}

func TestSessionSearchIncrementalSyncOnlyIndexesNewMessages(t *testing.T) {
	resolver := tempIndexResolver(t)
	all := []types.Message{
		msgText(types.RoleUser, "first alpha message"),
	}
	tool := SessionSearch(func() []types.Message { return all }, resolver)

	out1, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"alpha"}`))
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	var r1 []sessionSearchResult
	_ = json.Unmarshal([]byte(out1), &r1)
	if len(r1) != 1 {
		t.Fatalf("first search: got %d results, want 1: %s", len(r1), out1)
	}

	// Append a new message — the SAME tool instance (same resolver → same
	// on-disk index) should pick it up on the next call without losing the
	// earlier one.
	all = append(all, msgText(types.RoleAssistant, "second beta message"))
	tool2 := SessionSearch(func() []types.Message { return all }, resolver)

	outBeta, err := tool2.Execute(context.Background(), json.RawMessage(`{"query":"beta"}`))
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	var rBeta []sessionSearchResult
	_ = json.Unmarshal([]byte(outBeta), &rBeta)
	if len(rBeta) != 1 {
		t.Fatalf("expected the newly-appended message to be indexed and found: %s", outBeta)
	}

	outAlpha, err := tool2.Execute(context.Background(), json.RawMessage(`{"query":"alpha"}`))
	if err != nil {
		t.Fatalf("third search: %v", err)
	}
	var rAlpha []sessionSearchResult
	_ = json.Unmarshal([]byte(outAlpha), &rAlpha)
	if len(rAlpha) != 1 {
		t.Fatalf("original message must still be searchable after incremental sync: %s", outAlpha)
	}
}

func TestSessionSearchSurvivesPreCompactionHistory(t *testing.T) {
	// AllMessages() is defined to include everything, pre- and
	// post-compaction — SessionSearch must not special-case that; it just
	// indexes whatever the provider hands it.
	all := []types.Message{
		msgText(types.RoleUser, "a decision made long before compaction: use SQLite FTS5"),
		{Role: types.RoleAssistant, Parts: []types.ContentPart{{Text: "compacted summary follows"}}, Meta: &types.MessageMeta{IsCompaction: true}},
		msgText(types.RoleUser, "a much later question"),
	}
	tool := SessionSearch(func() []types.Message { return all }, tempIndexResolver(t))

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"SQLite"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var results []sessionSearchResult
	_ = json.Unmarshal([]byte(out), &results)
	if len(results) != 1 {
		t.Fatalf("pre-compaction content must still be searchable: %s", out)
	}
}

func TestSessionSearchNoMatchesReturnsEmptyArray(t *testing.T) {
	all := []types.Message{msgText(types.RoleUser, "hello world")}
	tool := SessionSearch(func() []types.Message { return all }, tempIndexResolver(t))

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"zzzznonexistent"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
}

func TestSessionSearchEmptySessionReturnsEmptyArray(t *testing.T) {
	tool := SessionSearch(func() []types.Message { return nil }, tempIndexResolver(t))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"anything"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
}

func TestSessionSearchRejectsEmptyQuery(t *testing.T) {
	tool := SessionSearch(func() []types.Message { return nil }, tempIndexResolver(t))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":""}`))
	if err == nil {
		t.Fatal("expected an error for empty query")
	}
}

func TestSessionSearchFallsBackToInMemoryWhenResolverReturnsEmpty(t *testing.T) {
	all := []types.Message{msgText(types.RoleUser, "gamma content here")}
	tool := SessionSearch(func() []types.Message { return all }, func() string { return "" })

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"gamma"}`))
	if err != nil {
		t.Fatalf("unexpected error with empty resolver (should fall back to :memory:): %v", err)
	}
	var results []sessionSearchResult
	_ = json.Unmarshal([]byte(out), &results)
	if len(results) != 1 {
		t.Fatalf("expected in-memory fallback to still work: %s", out)
	}
}

func TestSessionSearchLimitClamps(t *testing.T) {
	var all []types.Message
	for i := 0; i < 5; i++ {
		all = append(all, msgText(types.RoleUser, "repeated marker word appears here"))
	}
	tool := SessionSearch(func() []types.Message { return all }, tempIndexResolver(t))

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"marker","limit":2}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var results []sessionSearchResult
	_ = json.Unmarshal([]byte(out), &results)
	if len(results) != 2 {
		t.Fatalf("got %d results, want exactly 2 (limit)", len(results))
	}
}
