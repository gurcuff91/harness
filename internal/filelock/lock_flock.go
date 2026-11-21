//go:build linux || darwin || freebsd || netbsd || openbsd

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// kernelLocks: this platform has a real kernel lock (see the package doc).
const kernelLocks = true

// tryLockFile takes the lock if free: (true, nil); (false, nil) when another
// open file holds it.
func tryLockFile(f *os.File) (bool, error) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK):
			return false, nil
		}
		return false, err
	}
}

// lockFile blocks until the lock is ours.
func lockFile(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func unlockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
