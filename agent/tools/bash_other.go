//go:build !unix && !windows

package tools

import (
	"errors"
	"os/exec"
)

// Fallback for platforms that are neither unix nor windows (e.g. js/wasm, plan9).
// Process groups and session detachment aren't available, so foreground timeout
// still works via the direct process kill, and background mode is refused with a
// clear error rather than silently leaking a non-detached child.

// setProcessGroup is a no-op: no process-group primitive on these platforms.
// Timeout still kills the direct process via killProcessGroup.
func setProcessGroup(cmd *exec.Cmd) {}

// setDetached reports that background detachment is unsupported here, so the
// Bash tool surfaces an actionable error instead of pretending it worked.
func setDetached(cmd *exec.Cmd) error {
	return errors.New("background execution is not supported on this platform")
}

// killProcessGroup kills the direct process (best-effort).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
