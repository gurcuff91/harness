//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group (Setpgid) so the
// whole tree can be signalled at once. The group id equals the child's PID.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the entire process group (negative PID),
// terminating the command and any children it spawned — including background
// jobs (`cmd &`) and nohup'd processes that would otherwise survive and keep
// the output pipe open past the timeout.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid → the process group led by cmd.Process.Pid.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
