//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
)

// detachedProcess is the Win32 DETACHED_PROCESS creation flag (0x00000008). It
// isn't exported by the standard syscall package, so we define the constant
// directly (avoiding a golang.org/x/sys dependency). Combined with
// CREATE_NEW_PROCESS_GROUP it fully detaches a child from the console — the
// Windows analogue of a new Unix session.
const detachedProcess = 0x00000008

// setProcessGroup puts the command in its own process group via
// CREATE_NEW_PROCESS_GROUP — the Windows analogue of Setpgid. taskkill /t can
// then terminate the whole tree at once (see killProcessGroup).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// setDetached fully detaches the command from the console
// (CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS) so it survives the tool call —
// the Windows analogue of Setsid. Always succeeds on Windows.
func setDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
	return nil
}

// killProcessGroup terminates the process and its entire child tree using
// `taskkill /f /t` (/t = tree), the Windows analogue of killing a Unix process
// group by negative PID.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/f", "/t", "/pid", strconv.Itoa(cmd.Process.Pid)).Run()
}
