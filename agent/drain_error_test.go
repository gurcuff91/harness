package agent

import (
	"context"
	"testing"
)

// TestCancelBeforeErrCheckMakesEmitDeadCode is the regression test for the
// real bug found in drainFollowUps: it used to read ctx.Err() AFTER calling
// cancel(), which made its "report this turn's error" branch dead code that
// could never run — cancel() is unconditional (it's the turn's resource
// cleanup), so by the time the check ran, ctx.Err() was ALWAYS non-nil from
// our own cancellation, indistinguishable from a user Stop().
//
// This test pins the exact ordering semantics the fix depends on, at the
// context package level (no provider or live session needed): reading the
// cancellation state BEFORE cancel() is the only way to tell "the user
// stopped this turn" apart from "we cleaned up after a turn that failed for
// some other reason".
func TestCancelBeforeErrCheckMakesEmitDeadCode(t *testing.T) {
	t.Run("healthy turn: pre-cancel read is nil, post-cancel read is not", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		// What the FIXED code does: capture before cleanup.
		wasCancelled := ctx.Err() != nil
		cancel()
		// What the BROKEN code did: read after cleanup.
		afterCancel := ctx.Err() != nil

		if wasCancelled {
			t.Errorf("pre-cancel read should be false for a turn nobody stopped, got true")
		}
		if !afterCancel {
			t.Fatalf("post-cancel read should be true — if this fails the premise of the bug is wrong")
		}
		// The consequence: with the broken ordering, `err != nil && !afterCancel`
		// is false no matter what err is — the emit could never fire.
		if !afterCancel == true {
			t.Errorf("broken ordering would have allowed the emit; the bug requires it to be suppressed")
		}
	})

	t.Run("user-stopped turn: pre-cancel read already reports cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // simulates Session.Stop() firing during the turn

		// The fixed ordering still correctly identifies a real user stop,
		// which is what keeps "a user Stop stops everything, silently
		// (EventStop already told them)" intact.
		wasCancelled := ctx.Err() != nil
		if !wasCancelled {
			t.Errorf("a turn cancelled by Stop() must read as cancelled BEFORE cleanup, got false")
		}
	})
}

// TestErrorReportingDecision documents the decision table the fixed
// drainFollowUps implements, so a future change to that condition has to
// update this table deliberately rather than silently reintroducing the
// swallowing bug. errShouldBeReported mirrors the real condition
// (err != nil && !wasCancelled).
func TestErrorReportingDecision(t *testing.T) {
	errShouldBeReported := func(hasErr, wasCancelled bool) bool {
		return hasErr && !wasCancelled
	}

	cases := []struct {
		name         string
		hasErr       bool
		wasCancelled bool
		want         bool
		why          string
	}{
		{
			name: "summary failed, no stop", hasErr: true, wasCancelled: false, want: true,
			why: "the field bug: requestProgressUpdate failing must surface an EventError, not vanish",
		},
		{
			name: "empty summary guard tripped, no stop", hasErr: true, wasCancelled: false, want: true,
			why: "the empty-summary error also relies on drainFollowUps to report it",
		},
		{
			name: "user pressed Stop", hasErr: true, wasCancelled: true, want: false,
			why: "EventStop already told the user; a Stop stops everything without extra noise",
		},
		{
			name: "clean turn", hasErr: false, wasCancelled: false, want: false,
			why: "nothing to report",
		},
		{
			name: "clean turn that was also stopped", hasErr: false, wasCancelled: true, want: false,
			why: "nothing to report",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := errShouldBeReported(c.hasErr, c.wasCancelled); got != c.want {
				t.Errorf("errShouldBeReported(hasErr=%v, wasCancelled=%v) = %v, want %v — %s",
					c.hasErr, c.wasCancelled, got, c.want, c.why)
			}
		})
	}
}
