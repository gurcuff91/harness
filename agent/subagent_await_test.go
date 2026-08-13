package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// awaitSubagentResult is the race guard between a sub-agent's ctx deadline and
// its own end-of-turn event. These tests exercise the exact decision matrix
// without needing a live provider (the executor's real select is now this
// pure helper).

func TestAwaitSubagentResult_SuccessReturnsNil(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("  the answer  ")
	done := make(chan error, 1)
	done <- nil // EventTurnEnd, ctx still live

	out, err := awaitSubagentResult(context.Background(), done, &buf)
	if err != nil {
		t.Fatalf("expected nil error on a clean finish, got %v", err)
	}
	if out != "the answer" {
		t.Errorf("out = %q, want trimmed %q", out, "the answer")
	}
}

func TestAwaitSubagentResult_ExplicitErrorPropagates(t *testing.T) {
	var buf strings.Builder
	sentinel := errors.New("provider exploded")
	done := make(chan error, 1)
	done <- sentinel

	_, err := awaitSubagentResult(context.Background(), done, &buf)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel error to propagate, got %v", err)
	}
}

// The core regression: ctx is ALREADY cancelled by deadline AND a TurnEnd(nil)
// is sitting in done. The naive select would pick done<-nil at random and
// report success. The guard must prefer ctx.Err() so the deadline surfaces.
func TestAwaitSubagentResult_TimeoutWinsOverRacingTurnEnd(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("partial work")

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done() // guarantee the deadline has already fired

	// Both branches are now ready: done has a nil (TurnEnd), ctx is cancelled.
	done := make(chan error, 1)
	done <- nil

	_, err := awaitSubagentResult(ctx, done, &buf)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout must win over a racing TurnEnd(nil): got %v, want DeadlineExceeded", err)
	}
}

// A user Stop is context.Canceled — still surfaced (so the tool sees a
// non-nil error), but it is NOT a timeout, so downstream isTimeout() rejects it.
func TestAwaitSubagentResult_UserStopSurfacesAsCanceled(t *testing.T) {
	var buf strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // user Stop
	<-ctx.Done()

	done := make(chan error, 1)
	done <- nil // TurnEnd still fires via defer

	_, err := awaitSubagentResult(ctx, done, &buf)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("user Stop must surface as context.Canceled, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("a user Stop must NOT be classified as a timeout")
	}
}

// Pure deadline with nothing in done (the executor's other select branch):
// ctx.Done() path must also carry DeadlineExceeded.
func TestAwaitSubagentResult_DeadlineWithNoEvent(t *testing.T) {
	var buf strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	done := make(chan error) // never fed
	_, err := awaitSubagentResult(ctx, done, &buf)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded from the ctx.Done() branch, got %v", err)
	}
}
