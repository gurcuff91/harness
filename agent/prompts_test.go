package agent

import "testing"

// TestBuildCompactionCheckpoint verifies the reminder is appended only when
// at least one recovery tool (memory or session search) is enabled, that it
// names the right tool(s) for each combination, and that it's
// self-contained (doesn't silently mutate the summary it's appended to).
func TestBuildCompactionCheckpoint(t *testing.T) {
	const summary = "Goal: refactor auth. Done: added middleware. Pending: tests."

	t.Run("neither enabled — summary unchanged", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, false, false)
		if got != summary {
			t.Errorf("hasMemory=false, hasSessionSearch=false must not alter the summary.\ngot:  %q\nwant: %q", got, summary)
		}
	})

	t.Run("memory only — reminder mentions MemoSearch, not SessionSearch", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, true, false)
		assertStartsWithSummary(t, got, summary)
		reminder := got[len(summary):]
		for _, tool := range []string{"MemoSearch", "MemoWrite", "MemoDelete"} {
			if !contains(reminder, tool) {
				t.Errorf("memory-only reminder should mention %s, got: %q", tool, reminder)
			}
		}
		if contains(reminder, "SessionSearch") {
			t.Errorf("memory-only reminder must not mention SessionSearch (not enabled), got: %q", reminder)
		}
	})

	t.Run("session search only — reminder mentions SessionSearch, not MemoSearch", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, false, true)
		assertStartsWithSummary(t, got, summary)
		reminder := got[len(summary):]
		if !contains(reminder, "SessionSearch") {
			t.Errorf("session-search-only reminder should mention SessionSearch, got: %q", reminder)
		}
		if contains(reminder, "MemoSearch") {
			t.Errorf("session-search-only reminder must not mention MemoSearch (not enabled), got: %q", reminder)
		}
	})

	t.Run("both enabled — reminder mentions both tools side by side, no prescribed order", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, true, true)
		assertStartsWithSummary(t, got, summary)
		reminder := got[len(summary):]
		for _, tool := range []string{"MemoSearch", "SessionSearch"} {
			if !contains(reminder, tool) {
				t.Errorf("both-enabled reminder should mention %s, got: %q", tool, reminder)
			}
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
