package config

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

// ── Cross-process file lock (shared by CredentialsManager and SettingsManager) ──
//
// Both managers are per-process singletons, but harness commonly runs several
// processes at once against the SAME ~/.harness files (TUI + Telegram + Slack
// + serve, or several TUI windows side by side). Each manager's own mu
// (sync.RWMutex) only serializes goroutines WITHIN one process — it says
// nothing about a second process reading or writing the same file at the same
// moment. This lock closes that gap for a read-modify-write cycle (reload the
// latest disk state → apply a change → persist) using ONE primitive shared by
// both files: os.OpenFile(O_CREATE|O_EXCL) is atomic across processes on
// every OS Go supports, no per-platform syscalls needed. A lock older than
// staleFileLockAge is treated as abandoned (a crashed holder) and reclaimed
// automatically — it never wedges every future write.
const (
	fileLockRetryDelay  = 20 * time.Millisecond
	fileLockMaxAttempts = 100
	// staleFileLockAge: a lock older than this is treated as abandoned (a
	// crashed holder) and reclaimed. It MUST exceed the worst-case legitimate
	// hold time, or an alive-but-slow holder gets its lock stolen mid-work —
	// exactly the bug this file was hardened for. The longest a holder keeps
	// the lock is claude_oauth's token refresh: with a 30s-bounded HTTP client
	// and the retry/backoff moved OUTSIDE the lock (only ONE refresh runs under
	// it, no sleeps), that worst case is ~30s. 45s = 30s + margin.
	staleFileLockAge = 45 * time.Second
)

// AcquireFileLock creates path+".lock" exclusively, retrying with backoff if
// another process holds it. Returns a release function the caller must call
// once done. Called EXACTLY ONCE per logical operation — never exposed to
// callers as a standalone primitive to hold across several steps (see
// CredentialsManager.UpdateCredential's doc comment for why a raw "give me the
// lock and let me call other locking methods inside" pattern was removed).
// Used internally by CredentialsManager.SetCredential/DeleteCredential/
// UpdateCredential and SettingsManager's Set*/Delete* methods — each acquires
// it once, for its own single read-modify-write, and releases before
// returning. Exported so agent/schedule.Store can use the SAME hardened
// primitive for schedules.json (a sibling package within harness, not a
// different module — the internal/ rule only blocks OTHER modules, so this
// isn't a boundary violation, the same way agent/agent.go and
// agent/session.go already import internal/config directly).
//
// Ownership guard: the lockfile carries a unique token (a UUID) written at
// creation. Both release AND stale-reclaim remove the lock ONLY if it still
// holds the token they expect — never a bare os.Remove. Without this, a holder
// that was (rightly or wrongly) reclaimed while slow would, on finishing,
// delete the RECLAIMER's freshly-created lock, letting a third party in while
// the reclaimer still worked — two processes in the critical section, which for
// a single-use OAuth refresh token means a double redemption and a permanent
// invalid_grant. Compare-then-remove makes a reclaim (already rare) non-cascading.
func AcquireFileLock(path string) (release func(), err error) {
	lockPath := path + ".lock"
	token := uuid.NewString()
	for attempt := 0; attempt < fileLockMaxAttempts; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = f.WriteString(token)
			f.Close()
			return func() { removeLockIfOwned(lockPath, token) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("config: create lock %s: %w", lockPath, err)
		}
		// Held by someone else. If it looks abandoned (older than the stale
		// threshold), reclaim it — but only by removing the SAME token we
		// observed, so two processes reclaiming at once can't both win and a
		// holder that revived in the meantime isn't stomped.
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleFileLockAge {
			if stale, readErr := os.ReadFile(lockPath); readErr == nil {
				removeLockIfOwned(lockPath, string(stale))
			}
			continue
		}
		time.Sleep(fileLockRetryDelay)
	}
	return nil, fmt.Errorf("config: timed out waiting for lock: %s", lockPath)
}

// removeLockIfOwned removes lockPath only if its current contents still match
// token. If the file was already reclaimed (different token) or deleted, it is
// left untouched — we never remove a lock we don't still own. A read error is
// treated as "not ours" and left alone.
func removeLockIfOwned(lockPath, token string) {
	cur, err := os.ReadFile(lockPath)
	if err != nil {
		return // already gone, or unreadable — nothing of ours to remove
	}
	if string(cur) == token {
		_ = os.Remove(lockPath)
	}
}
