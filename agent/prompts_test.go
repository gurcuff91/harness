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
