package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The bug: a backgrounded child (`&`) inherited the output pipe and kept
// CombinedOutput blocking far past the timeout. With process-group kill, the
// timeout must return promptly (~2s), not wait for the child (10s).
func TestBashTimeoutKillsBackgroundChild(t *testing.T) {
	bash := Bash(t.TempDir())
	input, _ := json.Marshal(bashInput{Command: "sleep 10 & echo started", Timeout: 2})

	start := time.Now()
	out, err := bash.Execute(context.Background(), input)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("timeout did not fire promptly: took %v (background child leaked)", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got err=%v out=%q", err, out)
	}
	t.Logf("returned in %v with: %q", elapsed.Round(time.Millisecond), out)
}

// Bash is the deliberate exception to the "no partial on timeout" rule: on
// timeout it must STILL surface the partial process output (diagnostic — the
// step that hung), and its message must be actionable (suggest a larger
// timeout / background).
func TestBashTimeoutKeepsPartialAndIsActionable(t *testing.T) {
	bash := Bash(t.TempDir())
	// Emit a line, then hang — the emitted line is the "partial" we must keep.
	input, _ := json.Marshal(bashInput{Command: "echo diagnostic-marker; sleep 10", Timeout: 2})

	out, err := bash.Execute(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got err=%v", err)
	}
	if !strings.Contains(out, "diagnostic-marker") {
		t.Errorf("Bash must keep partial output on timeout; got %q", out)
	}
	if !strings.Contains(out, "Timeout after") || !strings.Contains(out, "'timeout'") {
		t.Errorf("Bash timeout message must be actionable; got %q", out)
	}
}

// A fast command must still return normally (no timeout regression).
func TestBashNormalCompletion(t *testing.T) {
	bash := Bash(t.TempDir())
	input, _ := json.Marshal(bashInput{Command: "echo hello", Timeout: 5})
	out, err := bash.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello', got %q", out)
	}
}

// Context cancellation (Stop) must return promptly too.
func TestBashContextCancel(t *testing.T) {
	bash := Bash(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	input, _ := json.Marshal(bashInput{Command: "sleep 30", Timeout: 60})
	start := time.Now()
	_, err := bash.Execute(ctx, input)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("cancel did not return promptly: %v", time.Since(start))
	}
	if err == nil {
		t.Error("expected cancellation error")
	}
}
