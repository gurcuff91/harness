package config

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func lockTarget(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "credentials.json")
}

// A normal acquire/release cycle removes the caller's own lock.
func TestFileLock_NormalReleaseRemovesOwnLock(t *testing.T) {
	path := lockTarget(t)
	release, err := acquireFileLock(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("lock file should exist while held: %v", err)
	}
	release()
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Errorf("lock file should be gone after release, stat err = %v", err)
	}
}

// The core regression: a slow holder that was reclaimed while it worked must
// NOT delete the reclaimer's freshly-created lock when it finally releases.
// Without the ownership guard, release() was a bare os.Remove that stomped
// whatever lock currently existed — cascading a rare reclaim into two processes
// in the critical section (a double OAuth-token redemption).
func TestFileLock_ReleaseDoesNotStompReclaimedLock(t *testing.T) {
	path := lockTarget(t)
	lockPath := path + ".lock"

	// Process A acquires.
	releaseA, err := acquireFileLock(path)
	if err != nil {
		t.Fatalf("A acquire: %v", err)
	}

	// Simulate B reclaiming A's lock: overwrite the lockfile with B's own token.
	// (This is exactly what acquireFileLock's stale-reclaim branch does — take
	// over the file — but we force it directly so the test is deterministic and
	// doesn't wait 45s.)
	bToken := "B-owns-this-now"
	if err := os.WriteFile(lockPath, []byte(bToken), 0600); err != nil {
		t.Fatalf("simulate B reclaim: %v", err)
	}

	// A finishes and releases — must NOT remove B's lock.
	releaseA()

	cur, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("B's lock must survive A's release, but it's gone: %v", err)
	}
	if string(cur) != bToken {
		t.Errorf("lock contents = %q, want B's token %q — A stomped the reclaimer's lock", cur, bToken)
	}
}

// A stale lock (older than staleFileLockAge) is reclaimed; a fresh one is not.
func TestFileLock_ReclaimsOnlyStaleLocks(t *testing.T) {
	path := lockTarget(t)
	lockPath := path + ".lock"

	// Fresh foreign lock — must NOT be reclaimed (acquire times out).
	if err := os.WriteFile(lockPath, []byte("someone-else"), 0600); err != nil {
		t.Fatal(err)
	}
	// Shorten the wait: with fileLockMaxAttempts*retryDelay ≈ 2s and a fresh
	// lock, acquire should fail. We just assert it does not succeed quickly.
	done := make(chan error, 1)
	go func() { r, e := acquireFileLock(path); _ = r; done <- e }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("acquire should NOT succeed against a fresh foreign lock")
		}
	case <-time.After(10 * time.Second):
		t.Error("acquire did not return in time against a fresh lock")
	}

	// Now age the lock past the threshold — it should be reclaimed.
	old := time.Now().Add(-2 * staleFileLockAge)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := acquireFileLock(path)
	if err != nil {
		t.Fatalf("acquire should reclaim a stale lock: %v", err)
	}
	release()
}

// Mutual exclusion under contention: N goroutines hammering the lock, an atomic
// counter that must never exceed 1 inside the critical section. -race clean.
func TestFileLock_MutualExclusionUnderContention(t *testing.T) {
	path := lockTarget(t)
	var inside atomic.Int32
	var maxSeen atomic.Int32
	var wg sync.WaitGroup

	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 15 {
				release, err := acquireFileLock(path)
				if err != nil {
					continue
				}
				n := inside.Add(1)
				if n > maxSeen.Load() {
					maxSeen.Store(n)
				}
				if n > 1 {
					t.Errorf("two holders in the critical section at once: %d", n)
				}
				time.Sleep(time.Millisecond)
				inside.Add(-1)
				release()
			}
		}()
	}
	wg.Wait()
	if maxSeen.Load() > 1 {
		t.Errorf("max concurrent holders = %d, want 1", maxSeen.Load())
	}
}
