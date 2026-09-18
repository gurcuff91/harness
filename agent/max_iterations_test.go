package agent

import (
	"testing"

	"github.com/gurcuff91/harness/agent/store"
)

// ── validateMaxIterations ────────────────────────────────────────────────

func TestValidateMaxIterationsBounds(t *testing.T) {
	cases := []struct {
		n    int
		want bool
	}{
		{0, false},
		{-1, false},
		{1, true},
		{50, true},
		{1000, true},
		{1001, false},
		{5000, false},
	}
	for _, c := range cases {
		err := validateMaxIterations(c.n)
		if (err == nil) != c.want {
			t.Errorf("validateMaxIterations(%d): err=%v, want valid=%v", c.n, err, c.want)
		}
	}
}

// TestAgentNewClampsExplicitOverLimitInsteadOfErroring confirms agent.New's
// documented "never fails" contract: an explicit AgentOptions.MaxIterations
// above the ceiling is silently clamped, not rejected.
func TestAgentNewClampsExplicitOverLimitInsteadOfErroring(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore(), MaxIterations: 5000})
	defer a.Close()
	if a.MaxIterations() != maxMaxIterations {
		t.Errorf("MaxIterations() = %d, want clamped to %d", a.MaxIterations(), maxMaxIterations)
	}
}

// TestAgentNewLeavesFetchSummarizeMaxIterationsUnaffected confirms the
// floor of 1 (not higher) doesn't disturb fetchSummarizeMaxIterations (5),
// an internal value well within [1, 1000] already — this is really a
// documentation-as-test guard: if someone ever raises minMaxIterations
// above 5, this test starts failing and forces them to notice.
func TestAgentNewLeavesFetchSummarizeMaxIterationsUnaffected(t *testing.T) {
	if err := validateMaxIterations(fetchSummarizeMaxIterations); err != nil {
		t.Errorf("fetchSummarizeMaxIterations (%d) must remain a valid explicit value: %v", fetchSummarizeMaxIterations, err)
	}
}

// ── Session.SetMaxIterations ────────────────────────────────────────────

func TestSetMaxIterationsRejectsOutOfRange(t *testing.T) {
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

	before := sess.MaxIterations()
	if err := sess.SetMaxIterations(0); err == nil {
		t.Error("expected an error for max_iterations=0")
	}
	if err := sess.SetMaxIterations(1001); err == nil {
		t.Error("expected an error for max_iterations=1001")
	}
	if sess.MaxIterations() != before {
		t.Errorf("a rejected SetMaxIterations must not mutate session state: got %d, want unchanged %d", sess.MaxIterations(), before)
	}
}

// TestSetMaxIterationsTakesEffectAndPersists confirms a valid
// SetMaxIterations call (a) updates MaxIterations() immediately and (b)
// survives a resume — the FileStore-backed persistence path, since
// InMemoryStore's Meta() round-trip alone wouldn't prove real durability.
func TestSetMaxIterationsTakesEffectAndPersists(t *testing.T) {
	fs, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	a := New(AgentOptions{Store: fs})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := sess.ID()

	if err := sess.SetMaxIterations(777); err != nil {
		t.Fatalf("SetMaxIterations: %v", err)
	}
	if sess.MaxIterations() != 777 {
		t.Fatalf("MaxIterations() = %d immediately after Set, want 777", sess.MaxIterations())
	}
	sess.Close()

	resumed, err := a.ResumeSession(id)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	defer resumed.Close()
	if resumed.MaxIterations() != 777 {
		t.Errorf("resumed session MaxIterations() = %d, want the persisted override 777", resumed.MaxIterations())
	}
}

// TestResumeWithoutOverrideUsesAgentDefault confirms a session that never
// called SetMaxIterations resumes with the OWNING AGENT's default — the
// override is opt-in, not a silent floor/ceiling applied to everyone.
func TestResumeWithoutOverrideUsesAgentDefault(t *testing.T) {
	fs, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	a := New(AgentOptions{Store: fs, MaxIterations: 42})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := sess.ID()
	if sess.MaxIterations() != 42 {
		t.Fatalf("new session MaxIterations() = %d, want the agent default 42", sess.MaxIterations())
	}
	sess.Close()

	resumed, err := a.ResumeSession(id)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	defer resumed.Close()
	if resumed.MaxIterations() != 42 {
		t.Errorf("resumed session MaxIterations() = %d, want the agent default 42 (no override was ever set)", resumed.MaxIterations())
	}
}

// TestForkCarriesOverMaxIterationsOverride confirms ForkSession copies a
// parent's SetMaxIterations override onto the fork, same as it already does
// for Thinking.
func TestForkCarriesOverMaxIterationsOverride(t *testing.T) {
	fs, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	a := New(AgentOptions{Store: fs})
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
	if err := sess.SetMaxIterations(333); err != nil {
		t.Fatalf("SetMaxIterations: %v", err)
	}

	fork, err := a.ForkSession(sess.ID())
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	defer fork.Close()
	if fork.MaxIterations() != 333 {
		t.Errorf("forked session MaxIterations() = %d, want the parent's override 333", fork.MaxIterations())
	}
}
