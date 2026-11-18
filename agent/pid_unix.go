//go:build !windows

package agent

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this pid exists on this machine
// (signal 0: existence check, nothing is delivered). EPERM means it exists but
// belongs to someone else — alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
