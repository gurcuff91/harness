//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
)

// procLookup reads /proc/<pid>/stat: its existence says the process exists,
// field 22 (starttime, clock ticks since boot) never changes during its life.
// Without /proc (unusual) it falls back to signal 0 with no marker.
func procLookup(pid int) procInfo {
	if pid <= 0 {
		return procInfo{}
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if _, statErr := os.Stat("/proc/self/stat"); statErr != nil {
			return procInfo{exists: signalZero(pid)} // no /proc at all
		}
		return procInfo{} // /proc works and this pid is not in it
	}
	// The command name (field 2) is in parentheses and may contain spaces or
	// parentheses: the fields that follow start after the LAST ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procInfo{exists: true}
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3 (state)
	const starttime = 22 - 3
	if len(f) <= starttime {
		return procInfo{exists: true}
	}
	return procInfo{exists: true, start: f[starttime], startKnown: true}
}
