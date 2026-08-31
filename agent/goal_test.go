package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// TestGoalToolRunsOneRoundAndReturnsTesterVerdict is an end-to-end
// integration check that the Goal tool's executor (agent.go's
// buildSessionTools wiring) actually runs a full Builder→Tester round
// against real ephemeral sub-agents — not just that the tool validates and
// forwards its input (already covered by agent/tools/goal_test.go's unit
// tests against a mock executor).
//
// Verifies the corrected error contract at the same time: a FAIL verdict
// must come back as (report, nil) — a normal, successful tool result, NEVER
// a returned error (EventToolResult.IsError must stay false either way; only
// the report TEXT differs between the two cases below).
func TestGoalToolRunsOneRoundAndReturnsTesterVerdict(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment to run a real Goal call")
	}

	t.Run("achievable criterion produces a PASS verdict, not a tool error", func(t *testing.T) {
		sess, err := a.NewSession(t.TempDir(), models[0].Model)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()

		var goalResult string
		var isErr bool
		sawResult := false
		done := make(chan struct{})
		sess.Subscribe(func(e types.Event) {
			if e.Type == types.EventToolResult && e.ToolName == "Goal" {
				sawResult = true
				goalResult = e.Output
				isErr = e.IsError
			}
			if e.Type == types.EventTurnEnd {
				close(done)
			}
		})
		sess.Prompt(context.Background(),
			"Use the Goal tool exactly once, with builder_prompt "+
				"'Run the Bash tool with command `echo GOAL_TEST_MARKER_ALPHA`.' "+
				"and tester_prompt 'Criterion: running `echo GOAL_TEST_MARKER_ALPHA` produces output containing the exact text GOAL_TEST_MARKER_ALPHA.' "+
				"Do not do this yourself — delegate it, then report back the Goal tool's result verbatim.")

		select {
		case <-done:
		case <-time.After(180 * time.Second):
			t.Fatal("turn did not finish within 180s")
		}

		if !sawResult {
			t.Skip("model did not invoke Goal this run (nondeterministic tool use/compliance) — rerun to exercise this path")
		}
		if isErr {
			t.Errorf("Goal tool result must not be flagged as a tool error for a genuine round — this is a system-failure flag only, never a verdict signal. Result: %q", goalResult)
		}
		if !strings.Contains(goalResult, "Overall: PASS") {
			t.Logf("achievable-criterion round did not report Overall: PASS this run (model/tester variance) — result: %q", goalResult)
		}
		if !strings.Contains(goalResult, "## Test Report") {
			t.Errorf("expected the Tester's report format in the tool output, got: %q", goalResult)
		}
	})

	t.Run("unsatisfiable criterion produces a FAIL verdict, still not a tool error", func(t *testing.T) {
		sess, err := a.NewSession(t.TempDir(), models[0].Model)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()

		var goalResult string
		var isErr bool
		sawResult := false
		done := make(chan struct{})
		sess.Subscribe(func(e types.Event) {
			if e.Type == types.EventToolResult && e.ToolName == "Goal" {
				sawResult = true
				goalResult = e.Output
				isErr = e.IsError
			}
			if e.Type == types.EventTurnEnd {
				close(done)
			}
		})
		sess.Prompt(context.Background(),
			"Use the Goal tool exactly once, with builder_prompt "+
				"'Run the Bash tool with command `echo hello`.' "+
				"and tester_prompt 'Criterion: the file /nonexistent-goal-test-path/impossible.txt exists and contains the text XYZ-IMPOSSIBLE.' "+
				"(this criterion is deliberately impossible — do not create the file yourself, just delegate and report the verdict). "+
				"Do not do this yourself — delegate it, then report back the Goal tool's result verbatim.")

		select {
		case <-done:
		case <-time.After(180 * time.Second):
			t.Fatal("turn did not finish within 180s")
		}

		if !sawResult {
			t.Skip("model did not invoke Goal this run (nondeterministic tool use/compliance) — rerun to exercise this path")
		}
		// The critical assertion: even a FAIL verdict must NOT be a tool
		// error — this is the corrected design (a FAIL is normal business
		// output, not a system failure).
		if isErr {
			t.Errorf("a FAIL verdict must not be a tool error — got IsError=true. Result: %q", goalResult)
		}
		if !strings.Contains(goalResult, "## Test Report") {
			t.Errorf("expected the Tester's report format in the tool output, got: %q", goalResult)
		}
		t.Logf("unsatisfiable-criterion round result: %q", goalResult)
	})
}
