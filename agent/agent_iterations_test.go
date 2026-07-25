package agent

import "testing"

// TestDefaultMaxIterations locks in the SDK/one-shot-command default. A
// change here is a deliberate product decision, not an accident — this test
// exists so bumping it requires touching this file too.
func TestDefaultMaxIterations(t *testing.T) {
	a := New(AgentOptions{})
	if got := a.MaxIterations(); got != 50 {
		t.Errorf("default MaxIterations = %d, want 50", got)
	}
}

// TestExplicitMaxIterationsOverridesDefault verifies AgentOptions.MaxIterations
// is honored when set, and that only <= 0 falls back to the default.
func TestExplicitMaxIterationsOverridesDefault(t *testing.T) {
	a := New(AgentOptions{MaxIterations: 120})
	if got := a.MaxIterations(); got != 120 {
		t.Errorf("MaxIterations = %d, want 120 (explicit)", got)
	}

	a2 := New(AgentOptions{MaxIterations: 0})
	if got := a2.MaxIterations(); got != 50 {
		t.Errorf("MaxIterations = %d, want 50 (0 falls back to default)", got)
	}
}

// TestSubagentMaxIterationsIsCapped verifies a subagent's iteration budget is
// capped at subagentMaxIterations (50) regardless of how high the parent's
// own limit is — a subagent is a focused, delegated task and shouldn't get as
// much room as a parent configured for long interactive work (120).
//
// This can't call the Subagent tool end-to-end without a live provider, so it
// asserts the same min() the wiring uses — pinning the intended cap value and
// the "never exceed the parent's own limit either" direction, both of which
// would silently regress if subagentMaxIterations were ever bumped without
// noticing it no longer caps anything below a high parent value.
func TestSubagentMaxIterationsIsCapped(t *testing.T) {
	cases := []struct {
		name         string
		parentMax    int
		wantSubAgent int
	}{
		{"parent above cap (interactive transports: 120)", 120, 50},
		{"parent at cap", 50, 50},
		{"parent below cap (SDK default scenario)", 25, 25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := min(c.parentMax, subagentMaxIterations)
			if got != c.wantSubAgent {
				t.Errorf("min(parent=%d, subagentMaxIterations=%d) = %d, want %d",
					c.parentMax, subagentMaxIterations, got, c.wantSubAgent)
			}
		})
	}
	if subagentMaxIterations != 50 {
		t.Errorf("subagentMaxIterations = %d, want 50", subagentMaxIterations)
	}
}
