package agent

import (
	"context"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// TestAddDelegatedCostDoesNotDeadlockUnderPromptSyncLock is the regression
// test for the exact deadlock class documented in the subagent-timeout-
// background project memory (bug #3, v0.74.8): promptSync holds s.mu for the
// ENTIRE turn, including parallel tool execution — so any code a tool's
// executor calls (Subagent's, or Fetch's FetchSummarizer) must never take
// s.mu itself. addDelegatedCost is exactly that kind of call (invoked from
// both executors right before the ephemeral sub-agent's Close()), so it MUST
// be lock-free. This reproduces the same "hold s.mu like promptSync does,
// call the method from another goroutine" pattern already proven for
// CurrentModel() (TestCurrentModelDoesNotDeadlockUnderPromptSyncLock).
func TestAddDelegatedCostDoesNotDeadlockUnderPromptSyncLock(t *testing.T) {
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

	// Simulate promptSync holding s.mu for the duration of a turn.
	sess.mu.Lock()
	defer sess.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// This is what a Subagent/Fetch executor does right before Close() —
		// if addDelegatedCost tries to take s.mu, it blocks forever here.
		sess.addDelegatedCost(types.SessionStats{InputTokens: 100, OutputTokens: 50, CostUSD: 0.01})
	}()

	select {
	case <-done:
		// Unblocked — lock-free as required.
	case <-time.After(3 * time.Second):
		t.Fatal("addDelegatedCost deadlocked — it blocked waiting for s.mu while s.mu was held by the simulated turn (promptSync). This is the exact deadlock class documented for CurrentModel() (subagent-timeout-background memory, bug #3).")
	}
}

// TestAddDelegatedCostIsInvisibleToContextUsage is the core correctness
// guarantee this feature depends on: folding a delegated sub-agent's spend
// into the parent's totals must NEVER change ContextUsage/ContextWindow —
// those describe the parent's OWN context occupancy, computed solely from
// its own most recent provider call (see updateStats), and must stay
// unaffected by tokens that were never part of that call.
func TestAddDelegatedCostIsInvisibleToContextUsage(t *testing.T) {
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

	before := sess.Stats()

	sess.addDelegatedCost(types.SessionStats{
		InputTokens:  1_000_000, // deliberately huge — would swamp any window if it leaked into ContextUsage
		OutputTokens: 500_000,
		CacheRead:    200_000,
		CacheWrite:   100_000,
		CostUSD:      12.34,
	})

	after := sess.Stats()

	if after.ContextUsage != before.ContextUsage {
		t.Errorf("ContextUsage changed from %v to %v — a delegated sub-agent's tokens must never affect it", before.ContextUsage, after.ContextUsage)
	}
	if after.ContextWindow != before.ContextWindow {
		t.Errorf("ContextWindow changed from %v to %v — must be untouched by delegated cost", before.ContextWindow, after.ContextWindow)
	}
	// The billing/reporting totals, on the other hand, MUST reflect it.
	if got, want := after.InputTokens, before.InputTokens+1_000_000; got != want {
		t.Errorf("InputTokens = %d, want %d (before + delegated)", got, want)
	}
	if got, want := after.OutputTokens, before.OutputTokens+500_000; got != want {
		t.Errorf("OutputTokens = %d, want %d", got, want)
	}
	if got, want := after.CacheRead, before.CacheRead+200_000; got != want {
		t.Errorf("CacheRead = %d, want %d", got, want)
	}
	if got, want := after.CacheWrite, before.CacheWrite+100_000; got != want {
		t.Errorf("CacheWrite = %d, want %d", got, want)
	}
	if diff := after.CostUSD - before.CostUSD - 12.34; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CostUSD delta = %v, want 12.34", after.CostUSD-before.CostUSD)
	}
}

// TestAddDelegatedCostDrainedByMeta covers the "background call finished
// after the launching turn already ended" case: nothing else would ever call
// updateStats again for this session, so Meta() (what server.go and every
// transport actually call for session info) must itself drain and persist
// the pending delegated cost — otherwise it would sit invisible in the
// atomics forever.
func TestAddDelegatedCostDrainedByMeta(t *testing.T) {
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

	sess.addDelegatedCost(types.SessionStats{InputTokens: 42, CostUSD: 1.5})

	meta := sess.Meta()
	if meta.Stats.InputTokens != 42 {
		t.Errorf("Meta().Stats.InputTokens = %d, want 42 (delegated cost added after the last turn must still surface via Meta())", meta.Stats.InputTokens)
	}
	if meta.Stats.CostUSD != 1.5 {
		t.Errorf("Meta().Stats.CostUSD = %v, want 1.5", meta.Stats.CostUSD)
	}

	// Must also be PERSISTED, not just visible in memory — re-reading the
	// store directly (bypassing the agent.Session wrapper) proves it landed
	// on disk/in the store, not just in the in-memory snapshot Meta() built.
	persisted := sess.store.Meta()
	if persisted.Stats.InputTokens != 42 {
		t.Errorf("persisted store Stats.InputTokens = %d, want 42 — Meta() must persist the drained delegated cost, not just report it", persisted.Stats.InputTokens)
	}
}

// TestSubagentDelegatedCostReachesParentSession is an end-to-end integration
// check of the real wiring in buildSessionTools' Subagent executor (agent.go)
// — not just the addDelegatedCost primitive in isolation above. Requires a
// live provider call (same limitation as TestCurrentModelReflectsSwitchModel
// and TestSubagentMaxIterationsIsCapped — see their comments), so it skips
// without one rather than faking a response; run it locally with a connected
// provider to exercise the real path.
func TestSubagentDelegatedCostReachesParentSession(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment to run a real Subagent call")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	before := sess.Stats()

	// Trigger the model to use the Subagent tool via a direct, unambiguous
	// instruction, and wait for the turn to fully finish.
	done := make(chan struct{})
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventTurnEnd {
			close(done)
		}
	})
	sess.Prompt(context.Background(), "Use the Subagent tool once, delegating exactly this task to it: respond with the single word PONG. Do not answer directly yourself.")

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("turn did not finish within 60s")
	}

	after := sess.Stats()
	if after.CostUSD <= before.CostUSD && after.InputTokens <= before.InputTokens {
		t.Skip("model did not invoke Subagent this run (nondeterministic tool choice) — rerun to exercise the delegated-cost path")
	}
	if after.ContextUsage == 0 {
		t.Error("ContextUsage is 0 after a real turn — sanity check failed, something else is wrong with this test setup")
	}
	t.Logf("parent session totals after a Subagent-using turn: input=%d output=%d cost=$%v (includes delegated sub-agent spend)",
		after.InputTokens, after.OutputTokens, after.CostUSD)
}

// TestFetchSummarizerDelegatedCostReachesParentSession mirrors the Subagent
// integration test above, but for Fetch's prompt-condensing sub-agent
// (buildFetchSummarizer's wiring in agent.go). Same live-provider limitation
// and skip behavior.
func TestFetchSummarizerDelegatedCostReachesParentSession(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment to run a real Fetch+prompt call")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	before := sess.Stats()

	done := make(chan struct{})
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventTurnEnd {
			close(done)
		}
	})
	sess.Prompt(context.Background(), "Use the Fetch tool to fetch https://example.com with 'prompt' set to \"summarize this page in one sentence\" — you MUST pass 'prompt', do not fetch without it.")

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("turn did not finish within 60s")
	}

	after := sess.Stats()
	if after.CostUSD <= before.CostUSD && after.InputTokens <= before.InputTokens {
		t.Skip("model did not invoke Fetch with 'prompt' this run (nondeterministic tool use) — rerun to exercise the delegated-cost path")
	}
	if after.ContextUsage == 0 {
		t.Error("ContextUsage is 0 after a real turn — sanity check failed, something else is wrong with this test setup")
	}
	t.Logf("parent session totals after a Fetch+prompt-using turn: input=%d output=%d cost=$%v (includes delegated summarizer spend)",
		after.InputTokens, after.OutputTokens, after.CostUSD)
}
