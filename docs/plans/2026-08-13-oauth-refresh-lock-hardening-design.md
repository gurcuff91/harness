# OAuth Refresh Lock Hardening — Design

**Date:** 2026-08-13
**Status:** Approved, implementing
**Area:** `internal/providers/claude_oauth.go`, `internal/config/filelock.go`

## Problem

Users running multiple harness processes on one machine (several TUI windows +
Telegram + Slack + serve, all against the same `~/.harness/`) intermittently hit
a **permanent** `invalid_grant` on the claude-oauth provider during ACTIVE use:

```
oauth token: token refresh failed (run 'harness connect claude-oauth' to
re-authenticate): refresh HTTP 400 (https://console.anthropic.com/v1/oauth/token):
{"error": "invalid_grant", "error_description": "Refresh token not found or invalid"}
```

Only `harness connect claude-oauth` recovers it. Anthropic OAuth refresh tokens
are **single-use with rotation**: each refresh redeems the old token and issues a
new one. If the same refresh token is redeemed twice, the second attempt gets a
permanent `invalid_grant`. So the bug is: something redeems the same token twice.

## Root cause — premature lock reclaim (cross-process)

The cross-process file lock (`internal/config/filelock.go`) that serializes the
read-redeem-write refresh cycle reclaims a lock older than `staleFileLockAge`
(**5s**), assuming its holder crashed. But a legitimate refresh can hold the lock
LONGER than 5s:

1. `refresh()` used `http.Post` = `http.DefaultClient`, `Timeout: 0` (**infinite**).
2. It tried **2 endpoints in series**, each unbounded.
3. The entire 3-attempt retry with `1s→2s→4s` backoff ran **inside** the lock.

So under slow network, Process A holds the lock >5s → Process B stats the lock,
sees it "stale", **removes it and enters** → A and B are both in the critical
section, both read the same refresh token, both redeem it → one wins, the other
gets permanent `invalid_grant`. Worse, A's incondicional `release()` then removes
B's lock, cascading.

A prior fix (v0.76.11, `reloadIfStale`) closed cross-process READ staleness but
did NOT address premature lock reclaim — the 5s threshold being SMALLER than a
legitimate refresh's worst case was the open hole.

## Non-destructive verification

Probed both token endpoints with a deliberately-invalid refresh token (a fake
token cannot be "consumed"):

| Endpoint | Response |
|---|---|
| `platform.claude.com/v1/oauth/token` | `400 {"error":"invalid_grant",...}` — live |
| `console.anthropic.com/v1/oauth/token` | `400 {"error":"invalid_grant",...}` — live |

Both are alive and identical. The 2-endpoint fallback therefore provides no real
migration resilience today, but DOES expose a second corruption vector: if the
first endpoint redeems the token but its response is lost (timeout/proxy), the
fallback redeems the (now-consumed) token again against the second → self-inflicted
`invalid_grant`, no multi-process needed. The user's error came from the SECOND
endpoint (`console.anthropic.com`), confirming this path was reached.

## Fix — defense in depth: (1) + (2b) + (3)

### (1) Bounded refresh + single endpoint
- Drop the 2-endpoint fallback; use only `https://platform.claude.com/v1/oauth/token`
  (newest, confirmed live). Eliminates the intra-call double-redeem vector.
- Use a dedicated `http.Client{Timeout: 30s}` for refresh (not `http.DefaultClient`'s
  infinite timeout, nor the stream client's 5min). A refresh can no longer hang.

### (2b) Retry/backoff OUTSIDE the lock
- The lock protects only ONE atomic read-redeem-write attempt, not the sleeps
  between attempts. Structure:
  ```
  for attempt in 0..3:
      if attempt > 0: sleep(backoff)        # OUTSIDE the lock
      WithLock:
          syncFromDisk + re-check expiry     # did another process already refresh?
          if still expired: refresh() once + persist
      if success or permanent(auth) error: break
      # network error → next iteration, lock released during the sleep
  ```
- Lock hold drops to ~one bounded refresh (≤30s), no sleeps. The re-check under
  the lock is the key safety net: a process that waited while another refreshed
  sees the NEW token on disk and uses it WITHOUT refreshing — zero double-redeem.
- Fast path (token still valid) never touches the lock — preserved, critical for
  per-LLM-call latency.

### (3) Lock ownership guard
- `acquireFileLock` writes a unique token (a UUID — `google/uuid` already a dep)
  into the lockfile at creation.
- `release()` reads the lockfile and removes it ONLY if it still contains our
  token. If it differs (someone reclaimed us), we do NOT touch it — kills the
  cascade where a slow holder deletes the reclaimer's lock.
- The stale-reclaim branch also compare-then-removes: read the token, remove only
  if it matches what the stat saw, so two processes reclaiming the same old lock
  can't both win.
- Internal to `acquireFileLock`/`release`; the public signature is unchanged, so
  all 8 call sites (credentials Set/Delete/WithLock, settings Set*/Delete*) are
  unaffected and equally protected.

### Threshold realignment
- With (1) bounding a refresh to 30s and (2b) leaving a single refresh under lock
  (no sleeps), the legitimate worst-case hold is ~30s. Raise `staleFileLockAge`
  from **5s → 45s** (30s refresh + margin) so a slow-but-alive holder is never
  mistaken for a crash; reclaim now only fires on a real crash.

## Testing

- **(1)** unit: refresh targets only `platform.claude.com`; refresh client has 30s timeout.
- **(2b)** unit (injectable refresh fn): transient network error retries 3×, lock
  released between attempts; re-check uses a disk token that appeared during the
  wait instead of refreshing; `invalid_grant` aborts immediately (no retry).
- **(3)** `filelock_test.go`: ownership (A reclaimed by B, A's release leaves B's
  lock intact — reproduces the cascade and proves it's gone); normal release
  removes own lock; compare-then-reclaim; `-race` stress with N goroutines and an
  atomic counter asserting never >1 in the critical section.
- **Cross-process regression:** two `tokenManager`s vs one `credentials.json`,
  both expired, mocked rotating refresh — assert exactly ONE redemption of the old
  token, the other picks up the new from disk.
- **Manual:** `harness serve` + several TUIs, force a refresh, confirm no instance
  hits `invalid_grant`.

## Out of scope
- `instances.json` (already read fresh per access, no caching — unaffected).
- SettingsManager semantics (only inherits the hardened `acquireFileLock`).
