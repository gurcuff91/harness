//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The whole file is locked as one byte range far beyond any content (the
// .flock file is empty): LockFileEx locks ranges, not files.
const lockBytes = 1

func lockRange(f *os.File, flags uint32) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, lockBytes, 0, ol)
}

// kernelLocks: this platform has a real kernel lock (see the package doc).
const kernelLocks = true

// tryLockFile takes the lock if free: (true, nil); (false, nil) when another
// handle holds it.
func tryLockFile(f *os.File) (bool, error) {
	err := lockRange(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	}
	return false, err
}

// lockFile blocks until the lock is ours.
func lockFile(f *os.File) error { return lockRange(f, windows.LOCKFILE_EXCLUSIVE_LOCK) }

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockBytes, 0, ol)
}
