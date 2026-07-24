package agent

import "testing"

// TestBuildCompactionCheckpoint verifies the memory nudge is appended only
// when the session has memory enabled, and that the reminder text is
// self-contained (doesn't silently mutate the summary it's appended to).
func TestBuildCompactionCheckpoint(t *testing.T) {
	const summary = "Goal: refactor auth. Done: added middleware. Pending: tests."

	t.Run("no memory — summary unchanged", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, false)
		if got != summary {
			t.Errorf("hasMemory=false must not alter the summary.\ngot:  %q\nwant: %q", got, summary)
		}
	})

	t.Run("with memory — reminder appended", func(t *testing.T) {
		got := buildCompactionCheckpoint(summary, true)
		if got == summary {
			t.Error("hasMemory=true must append the memory reminder, got the summary unchanged")
		}
		wantPrefix := summary
		if len(got) <= len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
			t.Errorf("checkpoint must start with the original summary verbatim, got: %q", got)
		}
		gotSuffix := got[len(summary):]
		if gotSuffix != memoryCompactionReminder {
			t.Errorf("appended suffix = %q, want memoryCompactionReminder %q", gotSuffix, memoryCompactionReminder)
		}
	})

	t.Run("reminder mentions the memory tools", func(t *testing.T) {
		for _, tool := range []string{"MemoSearch", "MemoWrite", "MemoDelete"} {
			if !contains(memoryCompactionReminder, tool) {
				t.Errorf("memoryCompactionReminder should mention %s so the model knows which tool to use, got: %q", tool, memoryCompactionReminder)
			}
		}
	})
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
