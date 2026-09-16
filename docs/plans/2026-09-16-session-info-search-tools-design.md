# SessionInfo + SessionSearch tools — design

## Goal

Give the model direct, structured access to information about its own
session — something `MemoWrite`/`MemoSearch` only cover selectively (a
memory is a deliberate note the model chooses to write; it is never the
full conversational record). Two new built-in tools:

- **SessionInfo** — a snapshot of the session's own identity/config.
- **SessionSearch** — full-text search over the ENTIRE conversation
  history (both user and assistant text), including turns already
  folded into a compaction checkpoint. This directly addresses a real
  gap: after compaction, the model (and the user) only have the
  compaction summary to go on — if that summary flattened or dropped a
  detail, there was previously no way back to the original text. The
  session's own `.jsonl` log is append-only and never truncated on
  compaction, so the original text is always still there on disk —
  `SessionSearch` is what makes it reachable again.

Both tools are gated behind a single flag, `AgentOptions.EnableSessionInfo`
— they're two views of the same underlying concept ("information about
this session"), so one switch turns on both.

## SessionInfo

No input parameters. Returns a small JSON object built from the session's
already-in-memory `SessionMeta`, no extra I/O:

```json
{"id": "...", "cwd": "...", "name": "...", "model": "provider/model", "thinking": "high", "created_at": "..."}
```

Deliberately excludes: `CompactCount` (the model has no existing concept of
compaction exposed to it — introducing the count alone, without the
context of what compaction even means, would be a confusing half-fact),
`LastActiveAt` (redundant with a live session's own passage of time), and
all of `Stats` (cost/tokens/context-window usage — operational data that
belongs in the TUI footer only, never surfaced to the model, matching the
existing project convention of not exposing spend to the model).

## SessionSearch

### Input

```json
{"query": "<string, required>", "limit": "<int, optional, default 10>"}
```

### What gets indexed

Only plain text from `ContentPart.Text` on `RoleUser` and `RoleAssistant`
messages — no `tool_call`, no `tool_result`, no `thinking`. This mirrors
`agent/memory`'s own existing FTS5 store, which is also text-only, and
keeps the index focused on conversational content (explanations,
decisions, discussion) rather than mechanical tool I/O — matching the
actual motivation (compaction losing conversational nuance, not "what
command did I run").

The ENTIRE history is indexed, including everything before the most
recent compaction checkpoint — the underlying `.jsonl` is append-only and
never truncated (`Session.Reset()` is the only operation that clears it,
and that's an explicit, deliberate user action, not a compaction
side-effect).

There is no timestamp per message in the `.jsonl` today, so time-scoped
queries ("what did I say yesterday") are out of scope — not supported,
not attempted.

### Index storage: SQLite FTS5, one file per session

```
<baseDir>/<cwd-slug>/<session-id>.search.db
```

Lives right next to the session's existing `<session-id>.jsonl` and
`<session-id>.meta.json` — same lifecycle, same directory. `DeleteSession`
removes it exactly like it already removes the other two files: no new
"where do orphaned indexes live" problem, and a user who manually copies
or moves a session directory takes its search index along for free.

Schema:

```sql
CREATE VIRTUAL TABLE messages_fts USING fts5(role, text);
CREATE TABLE search_meta(key TEXT PRIMARY KEY, value TEXT);
-- search_meta stores a single row: key='last_indexed_byte_offset'
```

### Sync strategy: lazy, byte-offset incremental, entirely inside the tool

No hook into `AddMessage`, no change to `agent/store` at all — the store
stays the "dumb port" its own doc comment says it should be. All
synchronization logic is self-contained inside `SessionSearch`'s
`Execute`:

1. Open (or create) `<session-id>.search.db`.
2. Read `last_indexed_byte_offset` from `search_meta` (0 if the db is new).
3. Open the session's `.jsonl`, `Seek` directly to that byte offset —
   never re-reads or re-scans already-indexed content, so a 369MB session
   log costs nothing per search beyond its own genuinely new bytes since
   the last search.
4. Read line-by-line from there to EOF. Each line is one JSON `types.Message`;
   a corrupt/partial line (e.g. a process killed mid-write) is skipped, not
   fatal — mirrors the tolerance-to-corruption behavior already documented
   for this exact append-only-log shape.
5. For each successfully parsed message with `Role` user/assistant, extract
   the concatenated `Text` from its parts (skipping parts with no `Text`)
   and `INSERT` into `messages_fts`.
6. Update `last_indexed_byte_offset` to the file's current size.
7. Run the FTS5 query: `SELECT role, snippet(messages_fts, 1, '[', ']', '...', 10) FROM messages_fts WHERE messages_fts MATCH ? ORDER BY bm25(messages_fts) LIMIT ?`.

### Output

JSON array, one object per result:

```json
[{"role": "assistant", "snippet": "...the fix uses [PKCE] because..."}]
```

`snippet()` is FTS5's native fragment-with-highlight function — no custom
extraction code needed. `limit` caps result count (default 10, same
default philosophy as `WebSearch`).

### Error handling

- Empty/missing `.jsonl` (a session with zero messages yet): no error,
  returns `[]`.
- No matches: `[]`, not an error.
- `query` empty: tool error, same `requireFields` pattern as every other
  built-in.
- A corrupt line mid-file: skipped silently (this is expected, ordinary
  behavior for an append-only log that could theoretically be caught
  mid-write, not a surfaced warning).

## Wiring

- `AgentOptions.EnableSessionInfo bool` — a single flag gates BOTH tools
  (`SessionInfo` and `SessionSearch`), registered together in
  `buildSessionTools`, following the exact same `if a.opts.EnableX &&
  a.isToolAllowed(...)` pattern `EnableWebSearch` already established.
- Enabled by default in `newInteractiveAgent` (TUI, Telegram, Slack, ACP,
  `harness serve`) — same rationale as `WebSearch`: these are the
  long-running, real-conversation transports where "the model forgot
  something after compaction" is an actual, recurring problem.
- Both tools need the session's cwd-slug directory and session ID to
  locate `<session-id>.jsonl`/`<session-id>.search.db` — resolved once in
  `buildSessionTools`, same place the session's other cwd-scoped tools
  (Bash, Read, Write, Edit) already get their `cwd`.

## What does NOT change

- `agent/store` (`FileStore`, `InMemoryStore`, the `SessionStore`
  interface) — completely untouched. No new hook, no new method.
- `agent/memory` — a separate, independent system; `SessionSearch` does
  not replace or interact with it. Both can coexist: `MemoSearch` for
  deliberately-curated cross-session notes, `SessionSearch` for
  unstructured full-recall within the current session.
- No new dependency — `modernc.org/sqlite` (already a direct dependency
  for `agent/memory`) is reused for the per-session `.search.db` too.

## Tests

- `cwdSlug`/directory resolution: reuse existing helpers, no new test
  surface there.
- `SessionSearch` sync logic: a temp `.jsonl` fixture with several
  messages (user/assistant text, plus a message with a tool_call/
  tool_result to confirm those are correctly EXCLUDED from the index),
  first search indexes everything from offset 0; append more messages,
  search again, confirm only the new content was scanned (byte offset
  advanced correctly) and old+new content both remain searchable.
- Corrupt line mid-file: a fixture with one malformed JSON line followed
  by valid ones — confirm indexing continues past it without aborting.
- Empty session: `.jsonl` doesn't exist yet — `[]`, no error.
- `SessionInfo`: confirms the exact field set (present fields correct,
  excluded fields — `CompactCount`, `LastActiveAt`, `Stats` — genuinely
  absent from the JSON output, not just zero-valued).

All existing tests continue to pass (`go test ./...`), `go vet ./...`
clean.
