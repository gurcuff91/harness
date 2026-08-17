# Schedule store lock hardening — Design

**Date:** 2026-08-17
**Status:** Approved, implementing
**Area:** `agent/schedule/store.go`, `internal/config/filelock.go`

## Problem

Raised by Gus after the OAuth refresh lock hardening work (see
`oauth-refresh-lock-hardening` memory / `2026-08-13-oauth-refresh-lock-hardening-design.md`):
does `agent/schedule.Store` (`~/.harness/schedules.json`) have the same
cross-process concurrency exposure `credentials.json` had before it was
hardened? Audited — yes, and structurally worse, because concurrent access here
isn't a rare edge case, it's the DOCUMENTED, EXPECTED architecture: "the agent
ALWAYS opens the store so Schedule* tools work in any session ... EnableScheduler
only decides whether this agent ALSO runs the engine" (`agent.go`'s own
comment). Running TUI + Telegram + Slack + `serve` simultaneously, each with its
own in-process `*Store`, with exactly one carrying `--scheduler`, is the
intended deployment shape — not a hypothetical.

`schedule.Store` has NONE of the protections `CredentialsManager` has:

| Protection | `CredentialsManager` (post-hardening) | `schedule.Store` (before this fix) |
|---|---|---|
| Cross-process read freshness (`reloadIfStale`) | ✅ | ❌ none — `List()` just returns in-memory `s.data` |
| Cross-process file lock on write | ✅ (`acquireFileLock`) | ❌ none — `Set`/`Delete`/`RecordRun` only hold `s.mu` (intra-process only) |
| Atomic read-modify-write (`UpdateCredential`) | ✅ | ❌ none — writers never re-read disk before applying their change |

### Two concrete bugs this causes

1. **Lost write** — two processes both read `schedules.json`, apply their own
   change in memory, then `save()` (a bare `os.WriteFile` of the WHOLE map).
   Last writer wins, silently discarding the other's change — same failure
   mode as pre-hardening `credentials.json`, but here triggered routinely: the
   Engine calls `RecordRun` every 30s in the `--scheduler` process, while ANY
   other process's session can call `Schedule`/`ScheduleDelete` at any time.
2. **Corrupted/lost audit trail** — `RecordRun`'s `save()` persists the
   ENTIRE in-memory map, not just the touched schedule. If that map is stale
   (no `reloadIfStale` equivalent), a `RecordRun` can silently overwrite an
   edit another process just made to a DIFFERENT schedule.

## Fix

### 1. Export the file lock primitive (no move, no duplication)

`internal/config/acquireFileLock` (already carries the UUID ownership guard and
45s stale-reclaim threshold hardened for OAuth) is exported as
`AcquireFileLock`. `agent/schedule` imports `internal/config` directly for it —
the same cross-package import `agent/agent.go` and `agent/session.go` already
do today. No extraction to a new package: the `internal/` compiler rule only
blocks OTHER modules, never sibling packages within harness itself, so there
was no real constraint forcing relocation.

### 2. `Store` gains cross-process read freshness

Same `loadedAt time.Time` + `os.Stat`-gated reload `CredentialsManager` uses.
`List()` calls it before returning, so the Engine (which calls `List()` every
tick), the `ToolAdapter` (`ScheduleList`), and the server's read endpoint all
see the freshest disk state, not a startup-time snapshot.

### 3. One atomic write entry point — `UpdateSchedule`, plus a trivial `Delete`

Following the `UpdateCredential` pattern (never repeat the "two parallel write
APIs" mistake from the OAuth work's first attempt):

```go
type UpdateAction int
const (
    ActionNoop  UpdateAction = iota // don't persist anything
    ActionWrite                      // persist next
)

// UpdateSchedule is the ONLY read-modify-write entry point for one (owner,
// slug) schedule. Takes the file lock exactly once, reloads the freshest
// on-disk state, and calls fn(current, ok) to decide the outcome.
func (s *Store) UpdateSchedule(
    owner, slug string,
    fn func(cur Schedule, ok bool) (next Schedule, action UpdateAction, err error),
) error
```

Delete is deliberately NOT expressed through this callback — it never needs to
inspect current state to decide anything (a model calls `ScheduleDelete(slug)`
with unconditional intent: "remove it if it's mine"), unlike `Set` (must
preserve `Runs`/`LastRun` from `cur`) and `RecordRun` (must increment fields
read fresh from `cur`). Forcing delete through a decision callback would be
fake generality for a case with no real decision. Considered and rejected: a
3rd `ActionDelete` enum value (adds a state `Set`/`RecordRun` never use) and a
`delete bool` return (a 4th return value, and `write=true `+`delete=true`
becomes a representable-but-invalid combination the type system doesn't
prevent). `Store.Delete(owner, slug)` stays a direct method: take the lock
once, reload, remove if present, persist, return whether it existed.

Callers become thin wrappers:
- `Set(slug, cron, prompt, owner)` → `UpdateSchedule`: validate the cron,
  build `next` preserving `cur.Runs`/`cur.LastRun` when `ok`, return
  `ActionWrite`.
- `RecordRun(slug, owner, at)` → `UpdateSchedule`: read the FRESH `cur` (fixes
  bug #2 above — no more mutating a possibly-stale in-memory copy), increment
  `Runs`/`LastRun`, return `ActionWrite`; if `!ok` (deleted by another process
  meanwhile) return `ActionNoop` — never resurrect a deleted schedule.
- `Delete(slug, owner)` → calls `Store.Delete` directly, no callback.

## Testing

- Unit tests mirroring `internal/config/update_credential_test.go`'s shape:
  missing-schedule reporting, `ActionNoop` skips persistence, `fn`'s own error
  aborts the write, a second `Store` instance sees the freshest disk state a
  first one just wrote (the lost-write regression, proven at the API level),
  N concurrent `UpdateSchedule` callers (separate `Store` instances = separate
  processes) never overlap in the critical section (`-race`).
- Regression specific to this store: `RecordRun` against a schedule another
  process just edited must persist the increment on top of the EDIT, not
  clobber it with a stale copy.
- `Delete` racing a concurrent `Set`/`RecordRun` on the same key resolves
  deterministically (whichever the lock serializes second wins, no corruption).

## Out of scope
- Making `Store` a per-process singleton (`sync.Once`-style `GetScheduleStore()`)
  — not needed: unlike `CredentialsManager`/`SettingsManager`, there is no
  existing global accessor to preserve, and each `Agent.New()` already opens
  its own `*Store` deliberately (one per agent instance, potentially several
  per process for subagents/tests). The lock + reload fixes work regardless of
  how many `*Store` instances point at the same file.
- Changing `Engine`'s polling/anchoring logic — unaffected by this work.
