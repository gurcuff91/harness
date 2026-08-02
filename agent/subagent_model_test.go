package agent

import (
	"testing"

	"github.com/gurcuff91/harness/agent/store"
)

// TestCurrentModelReflectsSwitchModel is the regression test for the bug
// where the Subagent tool kept spawning sub-agents against a session's
// ORIGINAL model even after /model (or ACP's session/set_config_option)
// switched it to a different one — e.g. a session created with a
// rate-limited provider, switched to a working one, but every subsequent
// Subagent call still hit the rate-limited model.
//
// Root cause: buildSessionTools' Subagent executor closure used to capture
// the "model" parameter — a plain string, frozen at the moment the
// session's tools were built (once, at NewSession/ResumeSession/
// ForkSession time) — and never looked at it again. SwitchModel updates
// Session.modelID/provider (and the persisted store), but never touched
// that already-built tool closure.
//
// The fix threads a **Session (sessRef) into buildSessionTools instead of a
// model string, so the closure can call (*sessRef).CurrentModel() at
// EXECUTION time. This test can't invoke the Subagent tool end-to-end
// without a live provider call (see TestSubagentMaxIterationsIsCapped's
// comment for the same limitation), but it exercises the exact mechanism
// the fix relies on: CurrentModel() must reflect a SwitchModel call made
// after the session — and therefore its tools, including Subagent's
// closure — were already built.
func TestCurrentModelReflectsSwitchModel(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 2 {
		t.Skip("need at least 2 active models in this environment to test switching between them")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if got := sess.CurrentModel(); got != models[0].Model {
		t.Fatalf("CurrentModel() = %q right after creation, want %q", got, models[0].Model)
	}

	if err := sess.SwitchModel(t.Context(), models[1].Model); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}

	if got := sess.CurrentModel(); got != models[1].Model {
		t.Errorf("CurrentModel() = %q after SwitchModel(%q), want %q — this is exactly what a Subagent call made AFTER a /model switch must see, not the original model",
			got, models[1].Model, models[1].Model)
	}
}
