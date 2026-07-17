//go:build unix

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

// setDetached starts the command in a NEW SESSION (Setsid), fully detaching it
// from the caller's controlling terminal and process group. Unlike Setpgid, a
// new session survives a `kill -pgid` of the caller's group — this is what lets
// a background command outlive the tool call. Go's Setsid works on macOS too,
// where the setsid(1) CLI does not exist. Always succeeds on unix.
func setDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
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
