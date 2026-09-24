package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// fakeSwitchModelProvider is a minimal providers.Provider stub for testing
// SwitchModel's compaction-model-selection logic. CompleteStream records
// which provider (by name) actually received the call, so tests can assert
// "did the ORIGIN or the DESTINATION end up compacting" without depending
// on a live model. failWith, when non-nil, makes CompleteStream return that
// error unconditionally (simulating a stale/exhausted provider — expired
// OAuth token, rate-limited, out of quota).
type fakeSwitchModelProvider struct {
	name          string
	contextWindow int
	failWith      error
	calls         *[]string // appended with f.name on every CompleteStream call
}

func (f *fakeSwitchModelProvider) CompleteStream(_ context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	*f.calls = append(*f.calls, f.name)
	if f.failWith != nil {
		return nil, f.failWith
	}
	if cb != nil {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "summary"})
	}
	return &types.Response{Text: "summary", Message: types.NewAssistantToolCallMessage("summary", "", "", nil)}, nil
}
func (f *fakeSwitchModelProvider) Name() string        { return f.name }
func (f *fakeSwitchModelProvider) DisplayName() string { return f.name }
func (f *fakeSwitchModelProvider) Description() string { return "fake" }
func (f *fakeSwitchModelProvider) IsActive() bool      { return true }
func (f *fakeSwitchModelProvider) Models() []types.ModelMeta {
	return []types.ModelMeta{{ID: "fake-model", ContextWindow: f.contextWindow}}
}
func (f *fakeSwitchModelProvider) FetchModels() ([]types.ModelMeta, error) { return f.Models(), nil }
func (f *fakeSwitchModelProvider) ModelMeta(modelID string) *types.ModelMeta {
	return &types.ModelMeta{ID: modelID, ContextWindow: f.contextWindow}
}
func (f *fakeSwitchModelProvider) CredentialType() types.CredentialType { return types.CredTypeNone }
func (f *fakeSwitchModelProvider) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: types.CredTypeNone}, nil
}
func (f *fakeSwitchModelProvider) ActivationSource() providers.ActivationSource {
	return providers.ActivationAuto
}
func (f *fakeSwitchModelProvider) Connect(types.Credentials) error { return nil }
func (f *fakeSwitchModelProvider) Disconnect() error               { return nil }

// newSwitchModelTestSession builds a real *Agent + *Session against two fake
// providers (origin, destination) registered into the global registry
// (snapshotted/restored by withCleanProviderRegistry), with the session
// already primed to a specific s.lastInputTokens so the SwitchModel
// mandatory/preventive checks have something concrete to evaluate.
func newSwitchModelTestSession(t *testing.T, originWindow, destWindow int, originFail error) (a *Agent, sess *Session, origin, dest *fakeSwitchModelProvider, calls *[]string) {
	t.Helper()
	withCleanProviderRegistry(t)

	calls = &[]string{}
	origin = &fakeSwitchModelProvider{name: "fake-origin", contextWindow: originWindow, calls: calls, failWith: originFail}
	dest = &fakeSwitchModelProvider{name: "fake-dest", contextWindow: destWindow, calls: calls}
	providers.All = append(providers.All, origin, dest)

	a = New(AgentOptions{Store: store.NewInMemoryStore()})
	var err error
	sess, err = a.NewSession(t.TempDir(), "fake-origin/fake-model")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return a, sess, origin, dest, calls
}

// setLastInputTokens simulates the session having already run a real turn
// that left it at the given token count — SwitchModel's decision logic
// reads s.lastInputTokens directly, so tests set it under s.mu the same way
// updateStats would after a real provider response.
func setLastInputTokens(sess *Session, n int) {
	sess.mu.Lock()
	sess.lastInputTokens = n
	sess.lastInputTokensVal.Store(int64(n))
	sess.mu.Unlock()
}

// TestSwitchModelCompactsWithDestinationWhenDestinationIsLarger confirms
// the ORIGINAL behavior (commit 8c09f06, the stuck-session-on-exhausted-
// origin fix) is preserved: switching to a model with a LARGER (or equal)
// context window compacts using the DESTINATION, not the origin.
func TestSwitchModelCompactsWithDestinationWhenDestinationIsLarger(t *testing.T) {
	// origin=100 tokens window (tiny, for a fast deterministic mandatory
	// trigger), dest=1000 — destination is strictly larger.
	_, sess, _, dest, calls := newSwitchModelTestSession(t, 100, 1000, nil)
	defer sess.Close()
	defer sess.agent.Close()

	setLastInputTokens(sess, 1200) // exceeds BOTH windows (mandatory trigger is evaluated against the destination's 1000) — dest is still the larger/preferred one to compact with

	if err := sess.SwitchModel(context.Background(), "fake-dest/fake-model"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != dest.name {
		t.Errorf("compaction calls = %v, want exactly [%q] (the destination)", *calls, dest.name)
	}
}

// TestSwitchModelCompactsWithOriginWhenDestinationIsSmaller is the
// regression test for the reported bug: switching to a model with a
// SMALLER context window than the origin must compact using the ORIGIN
// (which has more room for the full history), not the destination (which
// risks overflowing on the compaction-summary call itself — confirmed live
// against a real session that accumulated ~1.2M tokens on a 1.048M-window
// origin and hit "prompt is too long" switching to a 1.000M-window
// destination).
func TestSwitchModelCompactsWithOriginWhenDestinationIsSmaller(t *testing.T) {
	// origin=1000 (large), dest=100 (small) — destination is strictly smaller.
	_, sess, origin, _, calls := newSwitchModelTestSession(t, 1000, 100, nil)
	defer sess.Close()
	defer sess.agent.Close()

	setLastInputTokens(sess, 150) // exceeds dest's 100-token window — mandatory trigger; origin (1000) has plenty of room

	if err := sess.SwitchModel(context.Background(), "fake-dest/fake-model"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != origin.name {
		t.Errorf("compaction calls = %v, want exactly [%q] (the origin, since it's larger than the destination)", *calls, origin.name)
	}
}

// TestSwitchModelPreventiveCompactionTriggersBeforeMandatoryOverflow is the
// direct regression test for the reported bug's actual trigger: switching
// to a smaller-window model while ALREADY close to (but not yet over) the
// mandatory threshold must still compact preemptively — recomputing
// ContextUsage against the DESTINATION's window, not leaving it stale
// against the origin's (larger) one until the next real turn.
func TestSwitchModelPreventiveCompactionTriggersBeforeMandatoryOverflow(t *testing.T) {
	// origin=1000, dest=100. lastInputTokens=96 does NOT exceed dest's
	// window (mandatory trigger needs >100) but IS >= 95% of it (preventive
	// threshold) — this must still compact, using the origin (larger).
	_, sess, origin, _, calls := newSwitchModelTestSession(t, 1000, 100, nil)
	defer sess.Close()
	defer sess.agent.Close()

	setLastInputTokens(sess, 96) // 96/100 = 96% >= autoCompactThreshold (95%), but NOT > 100 (not the mandatory case)

	if err := sess.SwitchModel(context.Background(), "fake-dest/fake-model"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != origin.name {
		t.Errorf("compaction calls = %v, want exactly [%q] (preventive trigger, compacting with the larger origin)", *calls, origin.name)
	}
}

// TestSwitchModelNoCompactionWhenWellUnderThreshold confirms the fix is
// additive — a switch that doesn't approach either the mandatory or
// preventive threshold triggers NO compaction at all, exactly like before
// this change.
func TestSwitchModelNoCompactionWhenWellUnderThreshold(t *testing.T) {
	_, sess, _, _, calls := newSwitchModelTestSession(t, 1000, 1000, nil)
	defer sess.Close()
	defer sess.agent.Close()

	setLastInputTokens(sess, 50) // 5% of either window — nowhere near either threshold

	if err := sess.SwitchModel(context.Background(), "fake-dest/fake-model"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("compaction calls = %v, want none (usage well under threshold)", *calls)
	}
}

// TestSwitchModelNoFallbackWhenChosenOriginIsExhausted confirms the
// explicit design decision: if the origin is chosen to compact (because
// it's larger) but is itself stale/exhausted (e.g. an expired OAuth token,
// simulated here via failWith), SwitchModel does NOT silently retry
// against the destination — the real provider error propagates straight
// out, exactly as it did before the destination-compaction mechanism
// existed. This is a deliberate choice (confirmed with Gus): masking the
// origin's failure by quietly falling back to a smaller destination could
// itself overflow, and would hide that the origin needs reconnecting.
func TestSwitchModelNoFallbackWhenChosenOriginIsExhausted(t *testing.T) {
	exhausted := errors.New("401 unauthorized: token expired")
	_, sess, origin, _, calls := newSwitchModelTestSession(t, 1000, 100, exhausted)
	defer sess.Close()
	defer sess.agent.Close()

	setLastInputTokens(sess, 150) // mandatory trigger against the smaller destination; origin (larger) is chosen to compact, but it's exhausted

	err := sess.SwitchModel(context.Background(), "fake-dest/fake-model")
	if err == nil {
		t.Fatal("expected SwitchModel to fail when the chosen (origin) provider is exhausted, got nil")
	}
	if !errors.Is(err, exhausted) {
		t.Errorf("SwitchModel error = %v, want it to wrap the origin's real error (%v) — no silent fallback to the destination", err, exhausted)
	}
	// generateCompactionSummary retries transient-looking errors up to 3x
	// with backoff (this fake error doesn't match isContextOverflowError's
	// deterministic-failure classification, so it takes the retry path,
	// same as a real transient 401/429 would) — every attempt must still
	// target the origin only, never the destination.
	if len(*calls) == 0 {
		t.Fatal("compaction calls is empty, want at least one origin attempt")
	}
	for _, c := range *calls {
		if c != origin.name {
			t.Errorf("compaction calls = %v, want every call to be %q (only the origin attempted, no fallback to destination)", *calls, origin.name)
		}
	}
}
