package logx

import "testing"

// TestNewNilLoggerImplementsLogger verifies NewNilLogger returns a Logger
// that does genuinely nothing observable — no panics, no output.
func TestNewNilLoggerImplementsLogger(t *testing.T) {
	l := NewNilLogger()
	l.Info("x", "e", "k", "v")
	l.Warn("x", "e")
	l.Error("x", "e", "a", 1, "b", 2)
}
