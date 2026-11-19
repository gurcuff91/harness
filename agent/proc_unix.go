//go:build unix

package agent

import (
	"errors"
	"syscall"
)

// signalZero reports whether a process with this pid exists (signal 0 delivers
// nothing). EPERM means it exists but belongs to someone else: it exists.
func signalZero(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
