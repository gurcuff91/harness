//go:build !(linux || darwin || freebsd || netbsd || openbsd || windows)

package filelock

import "os"

// On a system without flock in x/sys (Solaris, illumos, AIX, Dragonfly…) the
// kernel lock is a no-op and the legacy O_EXCL lock alone serializes writers,
// as before 0.114.0. Without a kernel lock, a "flock:" .lock is NOT known to be
// an orphan (another process may be inside), so it is only reclaimed once stale.
const kernelLocks = false

func tryLockFile(*os.File) (bool, error) { return true, nil }
func lockFile(*os.File) error            { return nil }
func unlockFile(*os.File) error          { return nil }
