package config

import (
	"fmt"
	"os"
	"time"
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
	staleFileLockAge    = 5 * time.Second
)

// acquireFileLock creates path+".lock" exclusively, retrying with backoff if
// another process holds it. Returns a release function the caller must call
// (removing the lock file) once done. Shared by CredentialsManager.WithLock/
// SetCredential/DeleteCredential and SettingsManager's Set*/Delete* methods.
func acquireFileLock(path string) (release func(), err error) {
	lockPath := path + ".lock"
	for attempt := 0; attempt < fileLockMaxAttempts; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("config: create lock %s: %w", lockPath, err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleFileLockAge {
			_ = os.Remove(lockPath)
			continue
		}
		time.Sleep(fileLockRetryDelay)
	}
	return nil, fmt.Errorf("config: timed out waiting for lock: %s", lockPath)
}
