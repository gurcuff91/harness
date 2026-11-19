//go:build unix && !linux && !darwin

package agent

// procLookup on the other Unix systems: existence only, no start marker (a
// reused pid is then indistinguishable — the pre-marker behavior).
func procLookup(pid int) procInfo {
	if pid <= 0 {
		return procInfo{}
	}
	return procInfo{exists: signalZero(pid)}
}
