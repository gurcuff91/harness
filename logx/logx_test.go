package logx

import "testing"

// TestNilLoggerImplementsLogger is a compile-time-adjacent sanity check —
// the real assertion is the var _ Logger = NilLogger{} pattern used
// throughout the codebase's own implementations; this just documents that
// NilLogger satisfies its own package's interface too.
func TestNilLoggerImplementsLogger(t *testing.T) {
	var l Logger = NilLogger{}
	// Must not panic, and must genuinely do nothing observable.
	l.Info("x", "e", "k", "v")
	l.Warn("x", "e")
	l.Error("x", "e", "a", 1, "b", 2)
}
