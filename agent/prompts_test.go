package agent

import "testing"

// TestBuildCompactionCheckpoint verifies the reminder is appended only when
// persistent memory is enabled, and that it's self-contained (doesn't
// silently mutate the summary it's appended to).
func TestBuildCompactionCheckpoint(t *testing.T) {
	const summary = "Goal: refactor auth. Done: added middleware. Pending: tests."

	t.Run("memory disabled — summary unchanged", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, false)
		if got != summary {
			t.Errorf("hasMemory=false must not alter the summary.\ngot:  %q\nwant: %q", got, summary)
		}
	})

	t.Run("memory enabled — reminder mentions MemoSearch", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, true)
		assertStartsWithSummary(t, got, summary)
		reminder := got[len(summary):]
		// The reminder is about RECOVERING lost context (a read), so only
		// MemoSearch is relevant here — MemoWrite/MemoDelete (write-side
		// tools) aren't part of that operation and don't need to be named.
		if !contains(reminder, "MemoSearch") {
			t.Errorf("reminder should mention MemoSearch, got: %q", reminder)
		}
	})
}

// TestCompactSystemPromptWeightsRecencyAndRequiresNextStep is a contract
// test for the fix to a real reported bug: a session compacted MID-TURN
// (the common case — promptSync checks context usage at the top of every
// ReAct iteration, so compaction can fire while a task is still actively
// running) and the model "forgot" what it had just been doing, because the
// summary weighted the whole conversation uniformly instead of treating the
// most recent turn as unfinished, in-progress work. Confirmed live against
// a real provider: adding these two instructions measurably changed the
// summary's shape (a dedicated, concrete "next step" anchor instead of a
// vague restatement buried in "current state"). This test only guards
// against a FUTURE edit silently dropping either instruction — it can't
// verify the model actually follows them (that requires a live call, done
// once during design, not on every test run).
func TestCompactSystemPromptWeightsRecencyAndRequiresNextStep(t *testing.T) {
	if !contains(compactSystemPrompt, "Weight recency heavily") {
		t.Error("compactSystemPrompt must instruct the model to weight recency heavily — mid-turn compaction is the common case, and losing detail on the most recent (in-progress) turn is the exact bug this guards against")
	}
	if !contains(compactSystemPrompt, "## Immediate Next Step") {
		t.Error("compactSystemPrompt must require a dedicated \"## Immediate Next Step\" section — a concrete, anchored continuation point, not a restatement of the goal")
	}
}

func assertStartsWithSummary(t *testing.T, got, summary string) {
	t.Helper()
	if len(got) <= len(summary) || got[:len(summary)] != summary {
		t.Errorf("checkpoint must start with the original summary verbatim, got: %q", got)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
