package agent

import "os"

// procInfo is what the operating system says about one pid.
type procInfo struct {
	exists bool
	// start identifies the process's start (an OS-specific opaque marker:
	// ticks since boot on Linux, a timestamp elsewhere). Two processes that
	// ever had the same pid never share it. Meaningful only when startKnown.
	start      string
	startKnown bool
}

// lookupProc is the platform's lookup (proc_*.go); a variable for tests.
var lookupProc = procLookup

// ownStart is this process's start marker, recorded in every claim it makes
// ("" when the platform cannot tell; the claim then falls back to pid only).
var ownStart = func() string {
	if p := procLookup(os.Getpid()); p.startKnown {
		return p.start
	}
	return ""
}()

// ownerAlive decides whether the process that recorded a claim (pid + its
// start marker) is still running. A claim is the way to keep two sessions from
// answering the same conversation, so EVERY doubt resolves to "alive": taking a
// live owner's claim is worse than a conversation staying claimed.
//
//   - no process has pid               → dead
//   - claim has no marker (older harness) → alive if the pid exists (as before)
//   - the process's marker is unknown  → alive
//   - markers differ                   → dead: the pid was reused by another process
//   - markers match                    → alive
func ownerAlive(pid int, started string, look func(int) procInfo) bool {
	if pid <= 0 {
		return false
	}
	p := look(pid)
	switch {
	case !p.exists:
		return false
	case started == "", !p.startKnown:
		return true
	default:
		return p.start == started
	}
}
