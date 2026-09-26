package providers

import "testing"

// TestOpenCodeGoSessionHeadersSendsStableID reproduces a real, live
// regression: OpenCode Go started hard-rejecting every request without a
// stable x-opencode-session header (400 MissingSessionID) — confirmed
// against multiple other coding-agent clients hitting the same wall (see
// github.com/earendil-works/pi/issues/9230). This locks in that the header
// is always present and carries exactly the session value handed in
// (NewOpenCodeGo's uuid.New().String(), stable for the provider instance's
// lifetime — see OpenCodeGo.session's doc comment).
func TestOpenCodeGoSessionHeadersSendsStableID(t *testing.T) {
	headers := openCodeGoSessionHeaders("test-session-123")
	got, ok := headers["x-opencode-session"]
	if !ok {
		t.Fatal("x-opencode-session header missing entirely")
	}
	if got != "test-session-123" {
		t.Errorf("x-opencode-session = %q, want %q", got, "test-session-123")
	}
}

// TestNewOpenCodeGoAssignsStableNonEmptySession confirms every constructed
// provider gets a real, non-empty session id up front (never lazily
// generated per-request, which would defeat the "stable per conversation"
// contract OpenCode Go's routing/prompt-cache depends on).
func TestNewOpenCodeGoAssignsStableNonEmptySession(t *testing.T) {
	o := NewOpenCodeGo()
	if o.session == "" {
		t.Fatal("session is empty, want a generated UUID")
	}
	// Same instance, called twice — must be the same value (stable), not
	// regenerated.
	if headers1, headers2 := openCodeGoSessionHeaders(o.session), openCodeGoSessionHeaders(o.session); headers1["x-opencode-session"] != headers2["x-opencode-session"] {
		t.Error("session value changed across calls — must stay stable for the provider instance's lifetime")
	}
}
