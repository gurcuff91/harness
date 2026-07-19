package agent

import "testing"

// The scheduler routes a fired prompt to the session named by the schedule's
// owner. An inactive owner (or an empty one, from a stale schedule) resolves to
// nil — the prompt is dropped.
func TestResolveScheduledSession(t *testing.T) {
	a := &Agent{activeSessions: make(map[string]*Session)}
	s1 := &Session{id: "sess-1"}
	s2 := &Session{id: "sess-2"}
	a.activeSessions["sess-1"] = s1
	a.activeSessions["sess-2"] = s2

	if got := a.resolveScheduledSession("sess-2"); got != s2 {
		t.Error("owner should resolve to its session")
	}
	if got := a.resolveScheduledSession("ghost"); got != nil {
		t.Error("inactive owner should resolve to nil (prompt dropped)")
	}
	if got := a.resolveScheduledSession(""); got != nil {
		t.Error("empty owner (stale schedule) should resolve to nil")
	}
}

// Sessions register on create/resume and unregister on Close.
func TestSessionRegistration(t *testing.T) {
	a := &Agent{activeSessions: make(map[string]*Session)}
	s := &Session{id: "s", agent: a}
	a.registerSession(s)
	if a.resolveScheduledSession("s") != s {
		t.Fatal("registered session should be resolvable")
	}
	a.unregisterSession("s")
	if a.resolveScheduledSession("s") != nil {
		t.Error("unregistered session should be gone")
	}
}
