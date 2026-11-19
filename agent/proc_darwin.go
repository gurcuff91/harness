//go:build darwin

package agent

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// procLookup asks the kernel (sysctl kern.proc.pid) for the process's start
// time; existence comes from signal 0, which also covers another user's
// process. A process whose start cannot be read is reported with no marker.
func procLookup(pid int) procInfo {
	if pid <= 0 || !signalZero(pid) {
		return procInfo{}
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(k.Proc.P_pid) != pid {
		return procInfo{exists: true}
	}
	t := k.Proc.P_starttime
	return procInfo{exists: true, start: strconv.FormatInt(int64(t.Sec), 10) + "." + strconv.FormatInt(int64(t.Usec), 10), startKnown: true}
}
