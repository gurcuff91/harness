//go:build windows

package tools

import "os/exec"

// setProcessGroup is a no-op on Windows: Unix process groups don't apply.
// (A full job-object implementation could group children, but harness targets
// Unix shells; Windows support here is best-effort.)
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills the direct process on Windows. Detached children are
// not tracked without a job object, so this is best-effort.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
