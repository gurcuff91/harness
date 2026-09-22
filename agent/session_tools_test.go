package agent

import (
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// TestSessionToolsReturnsBuiltinDefinitions confirms Session.Tools() returns
// the same tool definitions the session actually sends to the provider —
// the exact set backing GET /api/sessions/{id}/tools (server.handleListTools)
// and client.Client.GetSessionTools.
func TestSessionToolsReturnsBuiltinDefinitions(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	defs := sess.Tools()
	if len(defs) == 0 {
		t.Fatal("expected at least the built-in tools to be registered")
	}

	names := map[string]bool{}
	for _, d := range defs {
		if d.Name == "" {
			t.Error("tool with empty name")
		}
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		names[d.Name] = true
	}
	for _, want := range []string{"Bash", "Read", "Write", "Edit", "Fetch"} {
		if !names[want] {
			t.Errorf("expected built-in tool %q, got %v", want, names)
		}
	}
}

// TestSessionToolsAlwaysIncludesSessionInfoUnlessDisallowed confirms
// SessionInfo is registered like any other built-in (Bash/Read/Write/Edit/
// Fetch) — no dedicated AgentOptions.EnableX flag, always present by
// default, and absent only when explicitly excluded via DisallowedTools.
func TestSessionToolsAlwaysIncludesSessionInfoUnlessDisallowed(t *testing.T) {
	aDefault := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer aDefault.Close()
	aDisallowed := New(AgentOptions{Store: store.NewInMemoryStore(), DisallowedTools: []string{"SessionInfo"}})
	defer aDisallowed.Close()

	models := aDefault.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sessDefault, err := aDefault.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession (default): %v", err)
	}
	defer sessDefault.Close()

	sessDisallowed, err := aDisallowed.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession (disallowed): %v", err)
	}
	defer sessDisallowed.Close()

	hasSessionInfo := func(defs []types.ToolDef) bool {
		for _, d := range defs {
			if d.Name == "SessionInfo" {
				return true
			}
		}
		return false
	}

	if !hasSessionInfo(sessDefault.Tools()) {
		t.Error("SessionInfo must appear in Tools() by default — no EnableX flag needed")
	}
	if hasSessionInfo(sessDisallowed.Tools()) {
		t.Error("SessionInfo must NOT appear in Tools() when explicitly excluded via DisallowedTools")
	}
}
