# SessionSearch removal + SessionInfo always-on

## Context

`SessionInfo` and `SessionSearch` were introduced together (see
`docs/plans/2026-09-16-session-info-search-tools-design.md` and
`docs/plans/2026-09-16-sessionsearch-into-store-design.md`) as "two views of
the same concept" — a session's own identity/config/usage (`SessionInfo`)
and full-text search over its complete conversation history
(`SessionSearch`, backed by a per-session SQLite FTS5 index in `FileStore`)
— gated behind a single `AgentOptions.EnableSessionInfo` flag.

After running in production for a while, Gus's own observation: the model
almost never reaches for `SessionSearch` in practice — it consistently
prefers persistent memory (`MemoWrite`/`MemoSearch`) to recover lost
context instead. The two tools solve overlapping problems (recovering
context that's no longer in the visible window), but memory is a curated,
deliberate recall mechanism the agent actively maintains, while
`SessionSearch` is a passive full-text index over raw conversation text —
and in practice the model just doesn't reach for the latter.

## Decision

Remove `SessionSearch` entirely — the tool, its home on the
`SessionStore` interface (`SearchMessages`, `SearchResult`,
`ErrSearchNotSupported`), both backend implementations (`FileStore`'s FTS5
index, `InMemoryStore`'s not-supported stub), and `Session.SearchMessages()`
— rather than leave dead infrastructure around because "it might be useful
someday." No externally-known SDK consumer implements `SessionStore`
outside this repo (no HTTP endpoint ever exposed it, no mention in the
README's documented API), so the removal's blast radius is genuinely
contained to this repo's own tests and the tool itself.

Since `SessionInfo` no longer shares a flag with anything, and it's a
small, purely informational, side-effect-free snapshot (no different in
kind from Bash/Read/Write/Edit/Fetch, which have never needed an
`AgentOptions.EnableX` gate), `AgentOptions.EnableSessionInfo` /
`harness.AgentWithSessionInfo()` are removed too — `SessionInfo` is now
registered unconditionally in `buildSessionTools()`, gated only by the
generic `isToolAllowed`/`DisallowedTools` mechanism every built-in already
respects.

## What changed

- `agent/tools/session.go`: `SessionSearch` (type, func, JSON schema) and
  its input struct/limit constants removed. Only `SessionInfoSnapshot` +
  `SessionInfo(provider)` remain.
- `agent/tools/names.go`: `ToolSessionSearch` removed.
- `agent/agent.go`: `AgentOptions.EnableSessionInfo` removed.
  `buildSessionTools()`'s SessionInfo registration moved from
  `if a.opts.EnableSessionInfo { if a.isToolAllowed(...) { ... } }` to a
  plain `if a.isToolAllowed(tools.ToolSessionInfo) { ... }` — same shape
  every other always-on built-in already uses. The system-prompt blurb
  ("## Session Tools") now only mentions `SessionInfo`, gated the same way.
- `agent/session.go`: `hasSessionSearch` field, the corresponding
  `newSession` parameter, and `Session.SearchMessages()` removed. The three
  `newSession(...)` call sites in `agent.go` (`NewSession`/`ResumeSession`/
  `ForkSession`) dropped their trailing `a.opts.EnableSessionInfo` argument.
- `agent/prompts.go`: `memoryCompactionReminder`/`buildCompactionCheckpoint`
  simplified from `(hasMemory, sessionSearchEnabled bool)` to a single
  `(hasMemory bool)` — the reminder only ever needs to point at persistent
  memory now.
- `agent/store/store.go`: `SearchResult`, `ErrSearchNotSupported`, and
  `SessionStore.SearchMessages` (the interface method) removed —
  **breaking change for any external `SessionStore` implementation**, though
  none is known to exist outside this repo. `Session.SearchMessages()`
  (the `*store.Session` domain-handle wrapper) removed too.
- `agent/store/file.go`: the entire FTS5 subsystem removed —
  `SearchMessages`, `openSearchIndexDB`, `searchIndexFilterVersion`,
  `resetSearchIndexIfStaleFilter`, `syncSearchIndex`,
  `searchSnippetMaxTokens`, `querySearchIndex`, `toSearchFTSQuery`,
  `deleteSearchIndex`, the per-session `searchMu`/`searchLocks` mutex map on
  `FileStore` itself, and the `database/sql`/`modernc.org/sqlite` imports
  that subsystem alone required in this file (still used elsewhere, e.g.
  `agent/memory`). `TruncateMessages` no longer calls `deleteSearchIndex`
  (nothing left to delete).
- `agent/store/in_memory.go`: `SearchMessages` (always
  `ErrSearchNotSupported`) removed.
- `harness.go`: `AgentWithSessionInfo()` facade option removed.
- `internal/cli/agent.go`: `newInteractiveAgent` no longer sets
  `EnableSessionInfo`.
- `internal/tui/toolfmt.go`/`output.go`: `SessionSearch`'s primary-param
  mapping and tool icon (`⟲`) removed.
- `README.md`/`AGENTS.md`: updated to drop `SessionSearch`/
  `EnableSessionInfo`/`AgentWithSessionInfo` mentions, and to describe
  `SessionInfo` as always-on (like the other built-ins) rather than
  flag-gated.

## Tests

- Removed entirely: `agent/store/search_test.go` (FTS5 sync/query/
  filter-version unit tests, no longer applicable), the `SessionSearch`
  half of `agent/tools/session_test.go`, `TestPortSearchMessagesCapability`
  and `TestSessionHandleSearchMessages` from `agent/store/store_test.go`,
  `TestSessionSearchMessagesDoesNotDeadlockUnderPromptSyncLock` from
  `agent/session_info_test.go`, and the `SessionSearch` case from
  `internal/tui/toolfmt_test.go`'s `TestFormatToolArgsBuiltins`.
- Rewritten: `agent/prompts_test.go`'s `TestBuildCompactionCheckpoint` now
  only covers the memory-on/memory-off cases (was four cases covering every
  combination of memory × session-search). `agent/session_info_test.go`'s
  integration test renamed `TestSessionInfoToolReachesRealTurn` (was
  `TestSessionInfoAndSessionSearchToolsReachRealTurn`), now only exercises
  `SessionInfo`. `agent/session_tools_test.go`'s
  `TestSessionToolsReflectsEnabledExtras` (which asserted SessionInfo was
  absent when `EnableSessionInfo` was unset) became
  `TestSessionToolsAlwaysIncludesSessionInfoUnlessDisallowed` — asserts the
  OPPOSITE default (SessionInfo present with no flag at all) plus the new
  way to opt out (`DisallowedTools`). `harness_test.go`'s
  `TestAgentWithBoolOptionsSetTheirFlag` dropped its `AgentWithSessionInfo`
  case.
- Full suite + `go vet` + `-race` (on `agent`, `agent/store`, `agent/tools`,
  `internal/tui`, and the root package) green; `gofmt -l` clean.
