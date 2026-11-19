//go:build windows

package agent

import (
	"errors"
	"strconv"
	"syscall"
)

// stillActive is the exit code Windows reports for a process that has not exited.
const stillActive = 259

// processQueryLimitedInformation is the least access right that lets us read
// the exit code and the process times (granted even for most elevated processes).
const processQueryLimitedInformation = 0x1000

// procLookup opens the process for a query:
//   - access denied → the process exists (someone else's or elevated), marker unknown;
//   - any other failure → no such process;
//   - opened, but its exit code is not STILL_ACTIVE → it has exited (a terminated
//     process stays an object while anyone holds a handle to it);
//   - otherwise its creation time is the marker.
func procLookup(pid int) procInfo {
	if pid <= 0 {
		return procInfo{}
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return procInfo{exists: true}
		}
		return procInfo{}
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err == nil && code != stillActive {
		return procInfo{}
	}
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return procInfo{exists: true}
	}
	return procInfo{exists: true, start: strconv.FormatInt(created.Nanoseconds(), 10), startKnown: true}
}
