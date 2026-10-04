// Package filelock is harness's cross-process advisory file lock — the one
// primitive every file-backed store uses to make a read-modify-write cycle
// atomic across several harness processes sharing the same ~/.harness files
// (TUI + Telegram + Slack + serve, or several TUI windows side by side).
//
// It lives in its own internal package so every file-backed store can share
// it without an import cycle: configstore.FileStore (settings.json /
// credentials.json) is its only user today.
package filelock

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

// A process's own sync.Mutex only serializes goroutines WITHIN that process —
// it says nothing about a second process reading or writing the same file at
// the same moment. This lock closes that gap using ONE portable primitive:
// os.OpenFile(O_CREATE|O_EXCL) is atomic across processes on every OS Go
// supports, no per-platform syscalls needed. A lock older than staleAge is
// treated as abandoned (a crashed holder) and reclaimed automatically — it
// never wedges every future write.
const (
	retryDelay  = 20 * time.Millisecond
	maxAttempts = 100
	// staleAge: a lock older than this is treated as abandoned (a crashed
	// holder) and reclaimed. It MUST exceed the worst-case legitimate hold
	// time, or an alive-but-slow holder gets its lock stolen mid-work —
	// exactly the bug this file was hardened for. The longest a holder keeps
	// the lock is an OAuth token refresh running inside
	// CredentialsStore.SwapValue: with a 30s-bounded HTTP client and the
	// retry/backoff OUTSIDE the lock (only ONE refresh runs under it, no
	// sleeps), that worst case is ~30s. 45s = 30s + margin.
	staleAge = 45 * time.Second
)

// Acquire creates path+".lock" exclusively, retrying with backoff if another
// process holds it. Returns a release function the caller must call once
// done. Meant to be taken EXACTLY ONCE per logical operation (one
// read-modify-write) and released before returning — never held across
// calls that could try to take it again (it is not re-entrant).
//
// Ownership guard: the lockfile carries a unique token (a UUID) written at
// creation. Both release AND stale-reclaim remove the lock ONLY if it still
// holds the token they expect — never a bare os.Remove. Without this, a
// holder that was (rightly or wrongly) reclaimed while slow would, on
// finishing, delete the RECLAIMER's freshly-created lock, letting a third
// party in while the reclaimer still worked — two processes in the critical
// section, which for a single-use OAuth refresh token means a double
// redemption and a permanent invalid_grant. Compare-then-remove makes a
// reclaim (already rare) non-cascading.
func Acquire(path string) (release func(), err error) {
	lockPath := path + ".lock"
	token := uuid.NewString()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = f.WriteString(token)
			f.Close()
			return func() { removeIfOwned(lockPath, token) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("filelock: create %s: %w", lockPath, err)
		}
		// Held by someone else. If it looks abandoned (older than staleAge),
		// reclaim it — but only by removing the SAME token we observed, so
		// two processes reclaiming at once can't both win and a holder that
		// revived in the meantime isn't stomped.
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleAge {
			if stale, readErr := os.ReadFile(lockPath); readErr == nil {
				removeIfOwned(lockPath, string(stale))
			}
			continue
		}
		time.Sleep(retryDelay)
	}
	return nil, fmt.Errorf("filelock: timed out waiting for lock: %s", lockPath)
}

// removeIfOwned removes lockPath only if its current contents still match
// token. If the file was already reclaimed (different token) or deleted, it is
// left untouched — we never remove a lock we don't still own. A read error is
// treated as "not ours" and left alone.
func removeIfOwned(lockPath, token string) {
	cur, err := os.ReadFile(lockPath)
	if err != nil {
		return // already gone, or unreadable — nothing of ours to remove
	}
	if string(cur) == token {
		_ = os.Remove(lockPath)
	}
}
