# Move SessionSearch's FTS5 logic into the SessionStore port

## Context

`SessionSearch` (added earlier this same day — see
`2026-09-16-session-info-search-tools-design.md`) currently lives entirely
inside `agent/tools/session.go`: the tool itself opens a per-session SQLite
FTS5 index, migrates its schema, syncs it incrementally against
`Session.AllMessages()`, and runs the FTS5 query — all inside the tool's
`Execute` closure. The agent wires it up via two injected closures: a
`SessionMessageProvider` (returns the full message history) and a
`SessionSearchIndexPathResolver` (computes where the `.search.db` should
live, duplicating `FileStore`'s own directory-layout knowledge from outside
that package).

This placement is wrong: full-text search over a session's message log is
persistence logic, not tool logic. Today, ANY future `SessionStore`
implementation (a Postgres-backed port, an S3-backed port, a hosted SaaS
backend) that wants to support `SessionSearch` has no way to plug in its own
search strategy — it's hardcoded to FTS5-over-a-per-session-SQLite-file
inside a package (`agent/tools`) that has no business knowing about storage
layout at all. The fix: make search a first-class `SessionStore` capability,
the same way `AppendMessage`/`LoadMessages`/`TruncateMessages` already are.

## What changes

### 1. `SessionStore` interface (`agent/store/store.go`)

New method:

```go
// SearchMessages performs a full-text search over sessionID's complete
// message history (everything ever appended, including messages folded
// into a compaction checkpoint) and returns matching results, most
// relevant first, capped to limit. Implementations that cannot support
// full-text search return ErrSearchNotSupported — not an empty slice,
// so callers can tell "no matches" from "not supported" apart.
SearchMessages(sessionID string, query string, limit int) ([]SearchResult, error)
```

New exported type and sentinel error, alongside `SessionMeta`:

```go
// SearchResult is one match from SearchMessages.
type SearchResult struct {
    Role    string `json:"role"`
    Snippet string `json:"snippet"`
}

// ErrSearchNotSupported is returned by SearchMessages when the backend has
// no full-text search capability (e.g. InMemoryStore).
var ErrSearchNotSupported = errors.New("store: message search not supported by this backend")
```

This is the ONLY interface change. Every existing `SessionStore` method
keeps its exact current signature and behavior.

### 2. `InMemoryStore.SearchMessages` (`agent/store/in_memory.go`)

```go
func (m *InMemoryStore) SearchMessages(sessionID string, query string, limit int) ([]SearchResult, error) {
    return nil, ErrSearchNotSupported
}
```

No in-memory FTS implementation — `InMemoryStore` is for tests and the
SDK's no-persist mode; reimplementing FTS5-in-RAM buys nothing real and
adds a second search implementation to maintain. Backends that want search
support pay for it (like `FileStore` does); backends that don't, say so
honestly.

### 3. `FileStore.SearchMessages` (`agent/store/file.go`)

All the FTS5 logic currently in `agent/tools/session.go` moves here
verbatim (adjusted only for `sessionID` now being an explicit parameter
instead of coming from a captured closure), including every fix already
shipped today:

- Per-session index at `<baseDir>/<cwd-slug>/<session-id>.search.db`
  (`FileStore` already knows this layout — no more resolver closure needed
  from the agent).
- `busy_timeout(5000)` + `journal_mode(WAL)` on the DSN.
- **Reverting to SQLite's native `snippet()`** (per Gus's explicit call in
  this session) instead of the `highlight()` + manual `windowSnippet()`
  approach from v0.76.73: `snippet(messages_fts, 1, '[', ']', '...', 64)` —
  64 tokens is the hard ceiling `snippet()`'s own 5th argument accepts
  ("must be greater than zero and equal to or less than 64", confirmed live
  against the FTS5 docs and `modernc.org/sqlite` in the prior session).
  This trades away the roomier ~500+ char window `windowSnippet()` gave for
  real simplicity: no custom trimming code, no UTF-8/word-boundary edge
  cases to maintain, SQLite's own match-centering heuristic instead of ours.
  `windowSnippet()`, `sessionSearchSnippetMaxChars`, and the
  `highlight()`-based query are deleted entirely, not carried over.
- Incremental sync by message-count offset (via
  `port.LoadMessages(sessionID, 0)` — `FileStore` already exposes this,
  no new read path needed), skipping non-user/assistant messages and
  `Meta.IsCompaction`/`Meta.IsSystemGenerated` messages (the exact filter
  shipped in v0.76.74).
- `filter_version` stamp + `resetIndexIfStaleFilter`, unchanged in
  spirit — still needed for ANY future filtering-rule change, not just the
  compaction one already shipped. Version counter carries over as-is (no
  reason to bump it again; the rules aren't changing, only where they live).

**Concurrency — the mutex, scoped correctly:**

A dedicated `map[sessionID]*sync.Mutex`, used ONLY by `SearchMessages` —
entirely independent of `FileStore`'s existing `m.mu` (which guards
meta/log I/O for all sessions and must NOT be reused here, per Gus:
serializing search behind the store's global lock would block unrelated
sessions' ordinary reads/writes while a search runs).

```go
type FileStore struct {
    baseDir string
    mu      sync.Mutex // existing — guards meta/log I/O, untouched

    searchMu    sync.Mutex            // guards searchLocks map access only
    searchLocks map[string]*sync.Mutex // sessionID → lock for THAT session's .search.db
}
```

This mutex is required, not optional — confirmed live in this session by
re-running the exact concurrency probe from the v0.76.73 fix (N goroutines
opening a brand-new SQLite file with `busy_timeout(5000)+WAL` already set on
the DSN): **3–8 failures per 50 rounds at 2–4-way concurrency**, even with
the pragmas in place. `busy_timeout`+WAL protects ordinary read/write
contention on an ALREADY-CREATED database; it does not reliably protect the
very first `CREATE TABLE`/`CREATE VIRTUAL TABLE` migration when multiple
goroutines race to bootstrap a brand-new file. The mutex removes that race
outright; scoping it per-sessionID (not global) means a slow search on one
session's index never blocks any other session's search, append, or meta
read.

**`TruncateMessages` gains one new responsibility**: after truncating the
`.jsonl`, also delete `<session-id>.search.db` (and its `-wal`/`-shm`
sidecars, if present) when it exists. Rationale (Gus's call): after a
reset, the index's message-count offset points at content that no longer
exists — deleting and letting the next `SearchMessages` call rebuild from
scratch is simpler and more obviously correct than trying to reconcile a
stale offset against a truncated log.

### 4. `store.Session` — thin wrapper (`agent/store/store.go`)

```go
// SearchMessages full-text searches this session's complete message
// history via the underlying store. Returns ErrSearchNotSupported if the
// backend doesn't implement search.
func (s *Session) SearchMessages(query string, limit int) ([]SearchResult, error) {
    s.mu.Lock()
    id := s.id
    s.mu.Unlock()
    return s.port.SearchMessages(id, query, limit)
}
```

Mirrors `AllMessages()`'s locking shape exactly: briefly takes `s.mu` only
to read the immutable-for-the-handle's-lifetime `s.id`, then calls the port
with the lock released — so a search (however long it takes) never blocks
`promptSync`'s hold on `s.mu` for the rest of the turn, and vice versa.

### 5. `agent/tools/session.go` — collapses to a thin adapter

Deleted entirely: `openSessionSearchDB`, `resetIndexIfStaleFilter`,
`sessionSearchFilterVersion`, `syncSessionSearchIndex`,
`querySessionSearchIndex`, `windowSnippet`, `toSessionSearchFTSQuery`,
`sessionSearchLocks`/`lockSessionSearchIndex`, `SessionMessageProvider`,
`SessionSearchIndexPathResolver`, and the `modernc.org/sqlite` import — none
of it belongs in `agent/tools` anymore.

What replaces it:

```go
// SessionSearchFunc performs the actual search — the agent injects a
// closure over its own Session.SearchMessages(query, limit), keeping this
// tool free of any knowledge of WHICH SessionStore backend is behind it.
type SessionSearchFunc func(query string, limit int) ([]store.SearchResult, error)

func SessionSearch(search SessionSearchFunc) Tool {
    return Tool{
        Def: types.ToolDef{ /* unchanged Name/Description/InputSchema */ },
        Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
            // unchanged: parse args, requireFields, empty-query check, limit clamp
            results, err := search(args.Query, limit)
            if errors.Is(err, store.ErrSearchNotSupported) {
                return "", fmt.Errorf("this session's storage backend does not support search")
            }
            if err != nil {
                return "", fmt.Errorf("session search: %w", err)
            }
            if results == nil {
                results = []store.SearchResult{}
            }
            out, err := json.MarshalIndent(results, "", "  ")
            // ...
        },
    }
}
```

`agent/tools` importing `agent/store` for the `store.SearchResult` type and
`store.ErrSearchNotSupported` sentinel is correct and deliberate — verified
live (compiled `agent/tools` importing `agent/store`, no import cycle:
`agent/store` has zero dependency on `agent/tools`). This was previously
avoided only because the tool used to OWN the storage logic and needed to
stay backend-agnostic about WHERE the index file lived; now that
`FileStore` owns that entirely, the tool only needs the shared result type,
exactly the same way `client/types.go` already imports `agent/store` today
for `SessionMeta`.

`SessionInfo` (the sibling tool in this same file) is untouched.

### 6. `agent/agent.go` — wiring simplifies

Before:
```go
reg.Register(tools.SessionSearch(
    func() []types.Message { return (*sessRef).AllMessages() },
    a.sessionSearchIndexPath(sessionID, cwd),
))
```

After:
```go
reg.Register(tools.SessionSearch(func(query string, limit int) ([]store.SearchResult, error) {
    return (*sessRef).SearchMessages(query, limit)
}))
```

The `sessionSearchIndexPath` method (currently ~15 lines in `agent.go`,
computing `store.CwdSlug`+`store.DefaultSessionsDir` from outside
`agent/store`) is deleted entirely — `FileStore` already knows its own
layout internally and no longer needs it handed in from outside.

No change to the `EnableSessionInfo` gating, tool-allowlist check, or the
fact that both tools are registered/unregistered together. `InMemoryStore`
users still get the tool registered (gating is a session-level policy
decision, independent of backend capability) — they just get
`ErrSearchNotSupported`'s honest error on every call, exactly like today's
in-memory fallback would have degraded, but explicit instead of silent.

## What does NOT change

- `SessionInfo` tool and its wiring — completely untouched.
- The `EnableSessionInfo` flag, tool-allowlist gating, system prompt
  section, post-compaction reminder branches — all untouched (they gate
  the search FEATURE being offered to the model at all, orthogonal to
  which backend implements it).
- The on-disk index format/location (`<session-id>.search.db` next to
  `.jsonl`/`.meta.json`) — identical to today, just built by `FileStore`
  directly instead of via an injected path resolver.
- The compaction/system-generated message exclusion filter and its
  `filter_version` migration mechanism — carried over as-is.
- No new dependency — still `modernc.org/sqlite`, already used by
  `agent/memory` and (until this migration) `agent/tools`.

## Testing

- `agent/store/file_test.go` (or a new `agent/store/search_test.go`) gains
  the full test suite currently in `agent/tools/session_test.go`'s
  SessionSearch section, adapted to call `FileStore.SearchMessages`
  directly instead of going through the `Tool.Execute` JSON boundary:
  finds indexed text, excludes tool_call/tool_result, excludes
  compaction/system-generated messages, incremental sync only indexes new
  messages, survives pre-compaction history, empty/no-match returns empty
  (not an error), limit clamping, concurrent calls don't deadlock or error
  (the exact `-race`-covered probe from v0.76.73), and the
  filter-version-triggered rebuild (hand-seeding a raw pre-migration
  on-disk file, same as today's `TestSessionSearchRebuildsIndexBuiltUnderOlderFilterVersion`).
  New: a `TestTruncateMessagesDeletesSearchIndex` covering the new
  `TruncateMessages` behavior.
- `agent/store/in_memory_test.go` gains one small test:
  `SearchMessages` returns `ErrSearchNotSupported`.
- `agent/tools/session_test.go`'s SessionSearch section shrinks to
  testing the tool's OWN responsibilities only: input parsing/validation,
  limit clamping, delegating to the injected `SessionSearchFunc`, and
  translating `ErrSearchNotSupported` into the expected user-facing error
  message — using a fake `SessionSearchFunc` closure, no real SQLite
  involved at this layer anymore.
- Full existing regression suite (`go test ./...`, `go vet ./...`) must
  stay green; `-race` re-run specifically for the migrated concurrency
  test, matching the rigor already applied when these fixes first shipped.

## Migration note for already-existing `.search.db` files

None needed beyond what's already in place: `filter_version` handles
schema/filter drift, and the on-disk path/format doesn't change at all —
only which package's code touches that file. An existing `.search.db` from
before this refactor continues to work unmodified (assuming its
`filter_version` already matches, which it does post-v0.76.74).
