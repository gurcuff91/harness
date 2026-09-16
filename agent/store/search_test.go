package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gurcuff91/harness/types"
)

// ── FileStore.SearchMessages ────────────────────────────────────────────
//
// Migrated from agent/tools/session_test.go when SessionSearch's FTS5
// logic moved from the tool into the SessionStore port (see
// docs/plans/2026-09-16-sessionsearch-into-store-design.md). Tests call
// FileStore.SearchMessages directly instead of going through the
// Tool.Execute JSON boundary.

func newSearchTestStore(t *testing.T) *FileStore {
	t.Helper()
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	return fs
}

func seedSession(t *testing.T, fs *FileStore, id string, msgs []types.Message) {
	t.Helper()
	if err := fs.SaveMeta(newMeta(id, "/proj")); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	for _, m := range msgs {
		if err := fs.AppendMessage(id, m); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
}

func msgText(role types.MessageRole, text string) types.Message {
	return types.Message{Role: role, Parts: []types.ContentPart{{Text: text}}}
}

func msgToolCall(id, name string, argsJSON string) types.Message {
	return types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{
		{ToolCall: &types.ToolCall{ID: id, Name: name, Input: []byte(argsJSON)}},
	}}
}

func msgToolResult(id, output string) types.Message {
	return types.Message{Role: types.RoleUser, Parts: []types.ContentPart{
		{ToolResult: &types.ToolResult{ID: id, Output: output}},
	}}
}

func TestSearchMessagesFindsIndexedText(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{
		msgText(types.RoleUser, "how does PKCE verification work"),
		msgText(types.RoleAssistant, "PKCE uses a code verifier and challenge to prevent interception"),
	})

	results, err := fs.SearchMessages("s1", "PKCE", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (both messages mention PKCE): %+v", len(results), results)
	}
}

func TestSearchMessagesExcludesToolCallAndResult(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{
		msgToolCall("t1", "Bash", `{"command":"echo uniquemarker123"}`),
		msgToolResult("t1", "output containing uniquemarker123 too"),
		msgText(types.RoleUser, "unrelated text"),
	})

	results, err := fs.SearchMessages("s1", "uniquemarker123", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("tool_call/tool_result content must never be indexed, got: %+v", results)
	}
}

func TestSearchMessagesIncrementalSyncOnlyIndexesNewMessages(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{
		msgText(types.RoleUser, "first alpha message"),
	})

	r1, err := fs.SearchMessages("s1", "alpha", 10)
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if len(r1) != 1 {
		t.Fatalf("first search: got %d results, want 1: %+v", len(r1), r1)
	}

	if err := fs.AppendMessage("s1", msgText(types.RoleAssistant, "second beta message")); err != nil {
		t.Fatalf("append: %v", err)
	}

	rBeta, err := fs.SearchMessages("s1", "beta", 10)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if len(rBeta) != 1 {
		t.Fatalf("expected the newly-appended message to be indexed and found: %+v", rBeta)
	}

	rAlpha, err := fs.SearchMessages("s1", "alpha", 10)
	if err != nil {
		t.Fatalf("third search: %v", err)
	}
	if len(rAlpha) != 1 {
		t.Fatalf("original message must still be searchable after incremental sync: %+v", rAlpha)
	}
}

func TestSearchMessagesSurvivesPreCompactionHistory(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{
		msgText(types.RoleUser, "a decision made long before compaction: use SQLite FTS5"),
		{Role: types.RoleAssistant, Parts: []types.ContentPart{{Text: "compacted summary follows"}}, Meta: &types.MessageMeta{IsCompaction: true}},
		msgText(types.RoleUser, "a much later question"),
	})

	results, err := fs.SearchMessages("s1", "SQLite", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("pre-compaction content must still be searchable: %+v", results)
	}
}

// TestSearchMessagesExcludesCompactionAndSystemGeneratedMessages is the
// direct regression test for the field report: a compaction checkpoint
// ("Previous conversation summary: ...") is stored as a Role-user message
// (see CompactionMessage), so without this filter it would show up in
// results indistinguishable from something the human actually typed — pure
// agent-authored noise. Same deal for the max-iterations IsSystemGenerated
// progress-check prompt.
func TestSearchMessagesExcludesCompactionAndSystemGeneratedMessages(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{
		{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "Previous conversation summary: uniquemarkerXYZ was discussed"}}, Meta: &types.MessageMeta{IsCompaction: true}},
		{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "You've reached the maximum uniquemarkerXYZ number of tool calls"}}, Meta: &types.MessageMeta{IsSystemGenerated: true}},
		msgText(types.RoleUser, "a genuine question mentioning uniquemarkerXYZ too"),
	})

	results, err := fs.SearchMessages("s1", "uniquemarkerXYZ", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want exactly 1 (only the genuine user message): %+v", len(results), results)
	}
	if !strings.Contains(results[0].Snippet, "genuine question") {
		t.Errorf("the surviving result must be the genuine message, got: %+v", results)
	}
}

func TestSearchMessagesNoMatchesReturnsEmptySlice(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{msgText(types.RoleUser, "hello world")})

	results, err := fs.SearchMessages("s1", "zzzznonexistent", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestSearchMessagesEmptySessionReturnsEmptySlice(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", nil)

	results, err := fs.SearchMessages("s1", "anything", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestSearchMessagesUnknownSessionErrors(t *testing.T) {
	fs := newSearchTestStore(t)
	if _, err := fs.SearchMessages("nonexistent", "anything", 10); err == nil {
		t.Fatal("expected an error for a session that was never created")
	}
}

func TestSearchMessagesLimitIsRespected(t *testing.T) {
	fs := newSearchTestStore(t)
	var msgs []types.Message
	for i := 0; i < 5; i++ {
		msgs = append(msgs, msgText(types.RoleUser, "repeated marker word appears here"))
	}
	seedSession(t, fs, "s1", msgs)

	results, err := fs.SearchMessages("s1", "marker", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want exactly 2 (limit)", len(results))
	}
}

// TestSearchMessagesRebuildsIndexBuiltUnderOlderFilterVersion is the direct
// regression test for the field report's second half: a .search.db built
// BEFORE the compaction/system-generated exclusion fix already has those
// messages indexed as ordinary content, and the message-count-offset
// incremental sync would never revisit them on its own — so the poison
// would persist forever without a one-time forced rebuild.
func TestSearchMessagesRebuildsIndexBuiltUnderOlderFilterVersion(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{
		{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "Previous conversation summary: poisonmarker leaked in here"}}, Meta: &types.MessageMeta{IsCompaction: true}},
		msgText(types.RoleUser, "a genuine later question about something else entirely"),
	})

	// Locate the on-disk index path the same way FileStore itself would,
	// and hand-seed it as a pre-fix index: schema present, the compaction
	// message already indexed as if it were ordinary content, offset
	// advanced past it, and no filter_version row at all (the pre-fix
	// schema never wrote one) — the oldest possible on-disk shape this
	// migration must handle.
	jsonlPath, found := fs.findJSONLPath("s1")
	if !found {
		t.Fatal("expected session s1 to exist")
	}
	indexPath := filepath.Join(filepath.Dir(jsonlPath), "s1.search.db")

	seed, err := sql.Open("sqlite", indexPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE search_meta(key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("seed schema (meta): %v", err)
	}
	if _, err := seed.Exec(`CREATE VIRTUAL TABLE messages_fts USING fts5(role, text, tokenize='unicode61')`); err != nil {
		t.Fatalf("seed schema (fts): %v", err)
	}
	if _, err := seed.Exec(`INSERT INTO messages_fts(role, text) VALUES ('user', 'Previous conversation summary: poisonmarker leaked in here')`); err != nil {
		t.Fatalf("seed poisoned row: %v", err)
	}
	if _, err := seed.Exec(`INSERT INTO search_meta(key, value) VALUES ('last_indexed_count', '1')`); err != nil {
		t.Fatalf("seed offset: %v", err)
	}
	seed.Close()

	results, err := fs.SearchMessages("s1", "poisonmarker", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("compaction message must be purged on rebuild, got %d results: %+v", len(results), results)
	}

	results2, err := fs.SearchMessages("s1", "genuine", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results2) != 1 {
		t.Fatalf("genuine message must survive the rebuild, got %d results: %+v", len(results2), results2)
	}
}

// TestTruncateMessagesDeletesSearchIndex confirms the new TruncateMessages
// behavior: after a reset, the search index's message-count offset would
// point at content that no longer exists, so the index is deleted outright
// rather than left stale — the next SearchMessages call rebuilds it from
// scratch.
func TestTruncateMessagesDeletesSearchIndex(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{msgText(types.RoleUser, "marker content")})

	// Build the index.
	if _, err := fs.SearchMessages("s1", "marker", 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jsonlPath, _ := fs.findJSONLPath("s1")
	indexPath := filepath.Join(filepath.Dir(jsonlPath), "s1.search.db")
	if _, err := sql.Open("sqlite", indexPath); err != nil {
		t.Fatalf("expected index file to exist: %v", err)
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("expected index file to exist on disk: %v", err)
	}

	if err := fs.TruncateMessages("s1"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Errorf("expected index file to be deleted after TruncateMessages, stat err = %v", err)
	}

	// A search after truncate + fresh content must work correctly (rebuilds
	// from scratch, doesn't error on the missing index).
	if err := fs.AppendMessage("s1", msgText(types.RoleUser, "fresh content after reset")); err != nil {
		t.Fatalf("append: %v", err)
	}
	results, err := fs.SearchMessages("s1", "fresh", 10)
	if err != nil {
		t.Fatalf("unexpected error searching after truncate: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}
}

// ── Concurrency ──────────────────────────────────────────────────────────

// TestSearchMessagesConcurrentCallsDoNotDeadlockOrError is the direct
// regression test for the reported "database is locked (5) (SQLITE_BUSY)"
// error: the ReAct loop runs tool calls in PARALLEL, and every
// SearchMessages invocation opens a FRESH *sql.DB against the SAME on-disk
// index file, so concurrent calls on one session used to race SQLite's
// single-writer lock even with busy_timeout+WAL set (verified live: the
// race is in the very first CREATE TABLE/CREATE VIRTUAL TABLE migration of
// a brand-new file, which busy_timeout does not reliably protect against).
func TestSearchMessagesConcurrentCallsDoNotDeadlockOrError(t *testing.T) {
	fs := newSearchTestStore(t)
	var msgs []types.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, msgText(types.RoleUser, fmt.Sprintf("concurrent marker message number %d", i)))
	}
	seedSession(t, fs, "s1", msgs)

	const n = 12
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fs.SearchMessages("s1", "marker", 10)
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent SearchMessages call failed: %v", err)
		}
	}
}

// TestSearchMessagesDifferentSessionsDoNotBlockEachOther confirms the
// per-sessionID mutex scoping: searching two DIFFERENT sessions
// concurrently must not serialize on each other (only same-session calls
// share a lock).
func TestSearchMessagesDifferentSessionsDoNotBlockEachOther(t *testing.T) {
	fs := newSearchTestStore(t)
	seedSession(t, fs, "s1", []types.Message{msgText(types.RoleUser, "alpha marker")})
	seedSession(t, fs, "s2", []types.Message{msgText(types.RoleUser, "beta marker")})

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := fs.SearchMessages("s1", "marker", 10)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		_, err := fs.SearchMessages("s2", "marker", 10)
		errCh <- err
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}
}
