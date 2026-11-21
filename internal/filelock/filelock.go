// Package filelock is harness's cross-process lock — the one primitive every
// file-backed store uses to make a read-modify-write cycle atomic across
// several harness processes sharing the same ~/.harness files (TUI + Telegram
// + Slack + serve, several TUI windows, SDK embedders such as jade-kaiban).
//
// It lives in its own internal package so every file-backed store can share
// it without an import cycle: configstore.FileStore (settings.json /
// credentials.json) is its only user today.
//
// # Kernel lock (since 0.114.0)
//
// The lock is the operating system's own: flock(LOCK_EX) on Unix, LockFileEx
// on Windows, on path+".flock" — a file that is created once and NEVER
// deleted (deleting a file others may have open and locked would let two
// processes lock two different files). Three properties the previous
// O_EXCL-file lock lacked:
//
//   - The kernel releases it when the holder dies, SIGKILL included: no
//     orphan lock, nothing to reclaim, no "stale after 45 s" window during
//     which every write failed.
//   - Waiters BLOCK in the kernel instead of polling every 20 ms, so a
//     process writing in a burst cannot keep winning the race against one
//     that waits (measured: with 5 ms holds, 1 wait in 5 used to time out).
//   - A waiter waits as long as a legitimate holder may hold it: the longest
//     is an OAuth refresh inside CredentialsStore.SwapValue (a 30 s-bounded
//     HTTP call), so Acquire waits up to lockTimeout (2 min), not 2 s.
//
// # Legacy lock (transition)
//
// Binaries before 0.114.0 only know path+".lock", created with O_EXCL. While
// both kinds may share a home (an SDK embedder upgraded later than the CLI),
// Acquire ALSO takes that file, after the kernel lock. Among new processes it
// never waits (the kernel lock already serializes them); it only waits for an
// old process. A new process writes it with a "flock:" token, so a .lock
// orphaned by a new process killed mid-write is recognized and removed at
// once — whoever holds the kernel lock knows no other new process is inside.
// An old-format token is respected, and reclaimed only after staleAge (what
// old binaries do too). Drop this layer once no pre-0.114.0 binary can share
// a home.
package filelock

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// lockTimeout bounds how long Acquire waits for both locks. It must exceed
	// the longest legitimate hold (an OAuth refresh: a 30 s-bounded request).
	// A variable so tests can shorten it.
	lockTimeout = 2 * time.Minute

	// retryDelay is the polling interval of the LEGACY lock only.
	retryDelay = 20 * time.Millisecond
)

// staleAge: a legacy .lock written by an OLD binary and older than this is
// treated as abandoned and reclaimed — what old binaries themselves do, so
// both sides agree. It exceeds the worst legitimate hold (~30 s).
const staleAge = 45 * time.Second

// newTokenPrefix marks a legacy .lock written by a kernel-locking process.
const newTokenPrefix = "flock:"

// Acquire takes the cross-process lock of path, waiting up to lockTimeout.
// Returns a release function the caller must call once done. Meant to be
// taken EXACTLY ONCE per logical operation (one read-modify-write) and
// released before returning — never held across calls that could try to take
// it again (it is not re-entrant).
func Acquire(path string) (release func(), err error) {
	deadline := time.Now().Add(lockTimeout)
	unlock, err := kernelLock(path+".flock", deadline)
	if err != nil {
		return nil, err
	}
	releaseLegacy, err := acquireLegacy(path+".lock", deadline)
	if err != nil {
		unlock()
		return nil, err
	}
	return func() {
		releaseLegacy()
		unlock()
	}, nil
}

// kernelLock opens (creating) lockPath and locks it exclusively, waiting up
// to deadline. The wait runs in a goroutine because neither flock nor
// LockFileEx takes a timeout: if the deadline passes first, Acquire returns
// an error and the goroutine, whenever it does get the lock, releases it at
// once (closing the file) — a timed-out caller never leaks a held lock.
func kernelLock(lockPath string, deadline time.Time) (unlock func(), err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("filelock: open %s: %w", lockPath, err)
	}
	unlock = func() { _ = unlockFile(f); _ = f.Close() }

	got, err := tryLockFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("filelock: lock %s: %w", lockPath, err)
	}
	if got {
		return unlock, nil
	}

	// Contended: block in the kernel, bounded by the deadline.
	var mu sync.Mutex
	gaveUp := false
	done := make(chan error, 1)
	go func() {
		err := lockFile(f)
		mu.Lock()
		defer mu.Unlock()
		if gaveUp {
			f.Close() // releases the lock we got too late
			return
		}
		done <- err // buffered: sent under mu, so the waiter below sees it
	}()

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case err = <-done:
	case <-timer.C:
		mu.Lock()
		select {
		case err = <-done: // got it at the last moment
		default:
			gaveUp = true
			mu.Unlock()
			return nil, fmt.Errorf("filelock: timed out after %s waiting for lock: %s", lockTimeout, lockPath)
		}
		mu.Unlock()
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("filelock: lock %s: %w", lockPath, err)
	}
	return unlock, nil
}

// acquireLegacy takes the pre-0.114.0 O_EXCL lock file (see the package doc).
// The caller holds the kernel lock.
func acquireLegacy(lockPath string, deadline time.Time) (release func(), err error) {
	token := newTokenPrefix + uuid.NewString()
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = f.WriteString(token)
			f.Close()
			return func() { removeIfOwned(lockPath, token) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("filelock: create %s: %w", lockPath, err)
		}
		cur, readErr := os.ReadFile(lockPath)
		switch {
		case readErr == nil && kernelLocks && strings.HasPrefix(string(cur), newTokenPrefix):
			// Written by a kernel-locking process; we hold the kernel lock,
			// so that process is gone: an orphan. Remove it now.
			removeIfOwned(lockPath, string(cur))
			continue
		case readErr == nil:
			// An old binary's lock: reclaim it only once stale, as they do.
			if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleAge {
				removeIfOwned(lockPath, string(cur))
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("filelock: timed out after %s waiting for lock: %s (held by an older harness)", lockTimeout, lockPath)
		}
		time.Sleep(retryDelay)
	}
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
