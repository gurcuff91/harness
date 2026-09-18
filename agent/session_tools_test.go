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

// TestSessionToolsReflectsEnabledExtras confirms optional tools (gated by
// AgentOptions.EnableX) show up in Session.Tools() when enabled, and are
// genuinely absent when not — the same wiring SessionInfo/SessionSearch's
// own gating tests cover, exercised here through the Tools() getter
// instead.
func TestSessionToolsReflectsEnabledExtras(t *testing.T) {
	aOff := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer aOff.Close()
	aOn := New(AgentOptions{Store: store.NewInMemoryStore(), EnableSessionInfo: true})
	defer aOn.Close()

	models := aOn.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sessOff, err := aOff.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession (off): %v", err)
	}
	defer sessOff.Close()

	sessOn, err := aOn.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession (on): %v", err)
	}
	defer sessOn.Close()

	hasSessionInfo := func(defs []types.ToolDef) bool {
		for _, d := range defs {
			if d.Name == "SessionInfo" {
				return true
			}
		}
		return false
	}

	if hasSessionInfo(sessOff.Tools()) {
		t.Error("SessionInfo must NOT appear in Tools() when EnableSessionInfo is off")
	}
	if !hasSessionInfo(sessOn.Tools()) {
		t.Error("SessionInfo must appear in Tools() when EnableSessionInfo is on")
	}
}
