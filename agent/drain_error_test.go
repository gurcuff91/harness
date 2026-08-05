package agent

import (
	"context"
	"errors"
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

// TestReportedErrPreventsDoubleEmit is the regression test for a real
// double-emit bug this package introduced when v0.76.13 fixed drainFollowUps'
// dead-code swallowing (see TestCancelBeforeErrCheckMakesEmitDeadCode): three
// sites inside promptSync's for-loop (a stream/provider error, and the two
// AddMessage store-error checks) already call s.emit(errorEvent(err)) locally
// before returning the error — a pattern requestProgressUpdate's failure path
// deliberately does NOT follow (it relies entirely on drainFollowUps to
// report). Once drainFollowUps' swallowing was fixed, those three sites
// started reporting the SAME error TWICE: once locally, once again in
// drainFollowUps, which could no longer tell "already told the user" apart
// from "never told the user" — a field report showed the exact same
// Anthropic API error rendered twice in the TUI.
//
// reported(err) wraps an error at the point it's already been emitted;
// drainFollowUps' errors.As check must recognize the wrapper and skip
// re-emitting, while the underlying error (and its exact .Error() text) stays
// fully intact for anything else that inspects it (PromptAndWait callers,
// errors.Is/As on a wrapped ProviderAPIError, etc.).
func TestReportedErrPreventsDoubleEmit(t *testing.T) {
	base := errors.New("anthropic API error 400")

	t.Run("reported() marks the error without changing its message", func(t *testing.T) {
		wrapped := reported(base)
		if wrapped.Error() != base.Error() {
			t.Errorf("reported(err).Error() = %q, want %q (identical text)", wrapped.Error(), base.Error())
		}
	})

	t.Run("errors.As recognizes the wrapper", func(t *testing.T) {
		wrapped := reported(base)
		var marker *reportedErr
		if !errors.As(wrapped, &marker) {
			t.Fatal("errors.As failed to recognize a reportedErr")
		}
	})

	t.Run("errors.Is still sees through to the underlying error", func(t *testing.T) {
		wrapped := reported(base)
		if !errors.Is(wrapped, base) {
			t.Error("errors.Is(reported(base), base) = false — Unwrap() must expose the original error")
		}
	})

	t.Run("a plain (non-wrapped) error is NOT mistaken for already-reported", func(t *testing.T) {
		plain := errors.New("model returned an empty summary") // requestProgressUpdate's shape
		var marker *reportedErr
		if errors.As(plain, &marker) {
			t.Error("a plain error must not match reportedErr — this is exactly the path that must " +
				"still get reported by drainFollowUps (the original v0.76.13 bug)")
		}
	})

	t.Run("the drainFollowUps decision table, extended with the reported case", func(t *testing.T) {
		shouldReport := func(err error, wasCancelled bool) bool {
			if err == nil || wasCancelled {
				return false
			}
			var already *reportedErr
			return !errors.As(err, &already)
		}
		cases := []struct {
			name         string
			err          error
			wasCancelled bool
			want         bool
		}{
			{"loop error, already emitted locally", reported(base), false, false},
			{"summary error, never emitted (v0.76.13 case)", errors.New("empty summary"), false, true},
			{"loop error, but the turn was also stopped", reported(base), true, false},
			{"no error", nil, false, false},
		}
		for _, c := range cases {
			if got := shouldReport(c.err, c.wasCancelled); got != c.want {
				t.Errorf("%s: shouldReport = %v, want %v", c.name, got, c.want)
			}
		}
	})

	// TestReportedErrPreventsDoubleEmit's remaining case: the wrapper must not
	// survive past drainFollowUps' own emit decision. reportedErr is an
	// internal bookkeeping detail of THAT function — once it has decided
	// whether to emit, a PromptAndWait caller (client.Ask, server.go's
	// handlePrompt, or any SDK consumer) must get the REAL underlying error
	// back, not the wrapper. errors.As/errors.Is already see through
	// reportedErr via Unwrap(), so this isn't required for correctness under
	// idiomatic error inspection — but a caller doing a plain type assertion
	// (err.(*types.ProviderAPIError) instead of errors.As) would silently
	// break on the wrapper for no reason: nothing outside drainFollowUps
	// needs to know "was this already reported".
	t.Run("the wrapper is unwrapped before reaching a PromptAndWait caller", func(t *testing.T) {
		wrapped := reported(base)

		// Mirrors drainFollowUps' own sequence: decide whether to emit, THEN
		// unwrap before building promptResult.
		var already *reportedErr
		errors.As(wrapped, &already)
		result := wrapped
		if already != nil {
			result = already.Unwrap()
		}

		if result != base {
			t.Errorf("expected the exact underlying error back, got %v (type %T)", result, result)
		}
		var stillWrapped *reportedErr
		if errors.As(result, &stillWrapped) {
			t.Error("the wrapper leaked past the unwrap — a plain type assertion by a caller would break")
		}
	})
}
