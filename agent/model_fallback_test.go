package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// switchableProvider is a provider whose availability a test flips on and off —
// "the provider got disconnected". While off, IsActive() is false, exactly what
// a provider with removed credentials reports.
type switchableProvider struct {
	name          string
	on            atomic.Bool
	modelIDs      []string
	contextWindow int
	calls         atomic.Int64 // CompleteStream calls (compaction summaries)
}

func newSwitchable(name string, contextWindow int, models ...string) *switchableProvider {
	p := &switchableProvider{name: name, contextWindow: contextWindow, modelIDs: models}
	p.on.Store(true)
	return p
}

func (p *switchableProvider) CompleteStream(_ context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	p.calls.Add(1)
	if cb != nil {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "summary"})
	}
	return &types.Response{Text: "summary", Message: types.NewAssistantToolCallMessage("summary", "", "", nil)}, nil
}
func (p *switchableProvider) Name() string        { return p.name }
func (p *switchableProvider) DisplayName() string { return p.name }
func (p *switchableProvider) Description() string { return "fake" }
func (p *switchableProvider) IsActive() bool      { return p.on.Load() }
func (p *switchableProvider) Models() []types.ModelMeta {
	var out []types.ModelMeta
	for _, id := range p.modelIDs {
		out = append(out, types.ModelMeta{ID: id, ContextWindow: p.contextWindow})
	}
	return out
}
func (p *switchableProvider) FetchModels() ([]types.ModelMeta, error) { return p.Models(), nil }
func (p *switchableProvider) ModelMeta(id string) *types.ModelMeta {
	for _, m := range p.modelIDs {
		if m == id {
			return &types.ModelMeta{ID: id, ContextWindow: p.contextWindow}
		}
	}
	return nil
}
func (p *switchableProvider) CredentialType() types.CredentialType { return types.CredTypeNone }
func (p *switchableProvider) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: types.CredTypeNone}, nil
}
func (p *switchableProvider) ActivationSource() providers.ActivationSource {
	return providers.ActivationAuto
}
func (p *switchableProvider) Connect(types.Credentials) error { return nil }
func (p *switchableProvider) Disconnect() error               { return nil }
func (p *switchableProvider) Dialect() string                 { return "openai" }
func (p *switchableProvider) Upstream() (providers.Upstream, error) {
	return providers.Upstream{}, providers.ErrNoUpstream
}

// fallbackEnv isolates the registry and settings, and registers the providers.
// The registry is rebuilt to hold ONLY the given fakes, so "no other provider is
// connected" is a real, controllable situation (not whatever the machine has).
func fallbackEnv(t *testing.T, ps ...*switchableProvider) (*Agent, *config.SettingsManager) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
	providers.EnsureRegistry()
	list := make([]providers.Provider, len(ps))
	for i, p := range ps {
		list[i] = p
	}
	t.Cleanup(providers.SetForTest(list))
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	t.Cleanup(func() { a.Close() })
	return a, config.GetSettingsManager()
}

// savedSession creates a session on model, adds some history, closes it (so it
// is only on disk) and returns its id.
func savedSession(t *testing.T, a *Agent, model string) string {
	t.Helper()
	s, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatalf("NewSession(%s): %v", model, err)
	}
	for _, txt := range []string{"hello", "world"} {
		if err := s.store.AddMessage(types.NewUserTextMessage(txt)); err != nil {
			t.Fatal(err)
		}
	}
	id := s.ID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return id
}

// The normal case is untouched: its provider is there → its own model, no fallback.
func TestResumeKeepsItsOwnModelWhenAvailable(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	other := newSwitchable("fb-other", 1000, "o1")
	a, sm := fallbackEnv(t, saved, other)
	sm.SetActiveModel("fb-other/o1") // a default exists — must NOT be used
	id := savedSession(t, a, "fb-saved/m1")

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model() != "fb-saved/m1" || s.ModelFallback() != nil {
		t.Errorf("model=%q fallback=%+v, want its own model and no fallback", s.Model(), s.ModelFallback())
	}
}

// The reported bug: the session's provider was disconnected; the session must
// still open, on the configured default model, and say so.
func TestResumeFallsBackToTheActiveModelWhenItsProviderIsGone(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	other := newSwitchable("fb-other", 1000, "o1", "o2")
	a, sm := fallbackEnv(t, saved, other)
	sm.SetActiveModel("fb-other/o2")
	id := savedSession(t, a, "fb-saved/m1")

	saved.on.Store(false) // provider disconnected

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatalf("resume must succeed through a fallback, got: %v", err)
	}
	if s.Model() != "fb-other/o2" {
		t.Errorf("model = %q, want the active model fb-other/o2", s.Model())
	}
	f := s.ModelFallback()
	if f == nil || f.From != "fb-saved/m1" || f.To != "fb-other/o2" || !strings.Contains(f.Reason, "not active") {
		t.Errorf("fallback = %+v", f)
	}
	// the history came along intact
	if n := len(s.AllMessages()); n != 2 {
		t.Errorf("history has %d messages, want 2", n)
	}
	// and the new model is what is saved, in the meta the next resume reads
	if m := s.store.Meta().Model; m != "fb-other/o2" {
		t.Errorf("persisted model = %q, want fb-other/o2", m)
	}
}

// With no active_model configured the first available model is used — what a
// new session does.
func TestResumeFallsBackToTheFirstAvailableModelWhenNoActiveModel(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	other := newSwitchable("fb-other", 1000, "o1", "o2")
	a, _ := fallbackEnv(t, saved, other) // no active model set
	id := savedSession(t, a, "fb-saved/m1")
	saved.on.Store(false)

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model() != "fb-other/o1" || s.ModelFallback() == nil {
		t.Errorf("model=%q fallback=%+v, want fb-other/o1 with a fallback", s.Model(), s.ModelFallback())
	}
}

// An active_model that is itself unavailable is skipped, not fatal.
func TestResumeSkipsAnUnavailableActiveModel(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	dead := newSwitchable("fb-dead", 1000, "d1")
	other := newSwitchable("fb-other", 1000, "o1")
	a, sm := fallbackEnv(t, saved, dead, other)
	sm.SetActiveModel("fb-dead/d1")
	id := savedSession(t, a, "fb-saved/m1")
	saved.on.Store(false)
	dead.on.Store(false)

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model() != "fb-other/o1" {
		t.Errorf("model = %q, want the first AVAILABLE model fb-other/o1", s.Model())
	}
}

// A stale active_model naming a model its provider no longer offers is skipped too.
func TestResumeSkipsAnActiveModelThatNoLongerExists(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	other := newSwitchable("fb-other", 1000, "o1")
	a, sm := fallbackEnv(t, saved, other)
	sm.SetActiveModel("fb-other/retired-model")
	id := savedSession(t, a, "fb-saved/m1")
	saved.on.Store(false)

	s, err := a.ResumeSession(id)
	if err != nil || s.Model() != "fb-other/o1" {
		t.Fatalf("err=%v model=%v", err, s)
	}
}

// The model was retired but its provider is still connected: same fallback.
func TestResumeFallsBackWhenTheModelWasRetired(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1", "m2")
	a, sm := fallbackEnv(t, saved)
	sm.SetActiveModel("fb-saved/m2")
	id := savedSession(t, a, "fb-saved/m1")
	saved.modelIDs = []string{"m2"} // m1 retired

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model() != "fb-saved/m2" || s.ModelFallback() == nil || !strings.Contains(s.ModelFallback().Reason, "not available") {
		t.Errorf("model=%q fallback=%+v", s.Model(), s.ModelFallback())
	}
}

// The one case that must still refuse: the session's provider AND the default
// are both unavailable — and the error says so, naming both.
func TestResumeRefusesOnlyWhenNothingAtAllIsAvailable(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	dead := newSwitchable("fb-dead", 1000, "d1")
	a, sm := fallbackEnv(t, saved, dead)
	sm.SetActiveModel("fb-dead/d1")
	id := savedSession(t, a, "fb-saved/m1")
	saved.on.Store(false)
	dead.on.Store(false)

	_, err := a.ResumeSession(id)
	if !errors.Is(err, ErrNoModelAvailable) {
		t.Fatalf("err = %v, want ErrNoModelAvailable", err)
	}
	for _, want := range []string{"fb-saved/m1", "default", "connect a provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q — it must name the session's model, the default, and what to do", err, want)
		}
	}
	// nothing was changed on disk by the failed attempt
	if m, _, _ := a.store.LoadMeta(id); m.Model != "fb-saved/m1" {
		t.Errorf("a refused resume rewrote the model to %q", m.Model)
	}
	// and once a provider comes back, the session opens on its ORIGINAL model
	saved.on.Store(true)
	s, err := a.ResumeSession(id)
	if err != nil || s.Model() != "fb-saved/m1" || s.ModelFallback() != nil {
		t.Errorf("after reconnecting: err=%v model=%v", err, s)
	}
}

// No other provider connected at all (and no active model): refuse, with the
// explanation that nothing is there to fall back to.
func TestResumeRefusesWhenNoOtherProviderIsConnected(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	a, _ := fallbackEnv(t, saved)
	id := savedSession(t, a, "fb-saved/m1")
	saved.on.Store(false)

	_, err := a.ResumeSession(id)
	if !errors.Is(err, ErrNoModelAvailable) || !strings.Contains(err.Error(), "no other provider is connected") {
		t.Fatalf("err = %v", err)
	}
}

// Once resumed on the fallback, the session keeps it: reconnecting the original
// provider does NOT silently flip the session back to a model it left.
func TestFallbackModelIsPersistedAcrossResumes(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	other := newSwitchable("fb-other", 1000, "o1")
	a, sm := fallbackEnv(t, saved, other)
	sm.SetActiveModel("fb-other/o1")
	id := savedSession(t, a, "fb-saved/m1")
	saved.on.Store(false)

	s, err := a.ResumeSession(id)
	if err != nil || s.ModelFallback() == nil {
		t.Fatalf("first resume: %v", err)
	}
	s.Close()
	saved.on.Store(true) // the provider comes back

	s2, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Model() != "fb-other/o1" {
		t.Errorf("model = %q: the session must stay on the model it was moved to", s2.Model())
	}
	if s2.ModelFallback() != nil {
		t.Errorf("a plain resume of an already-moved session must not report a fallback: %+v", s2.ModelFallback())
	}
}

// An already-active session is returned as it is (idempotent): no re-resolution.
func TestResumeOfAnActiveSessionDoesNotReResolve(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	a, _ := fallbackEnv(t, saved)
	s, err := a.NewSession(t.TempDir(), "fb-saved/m1")
	if err != nil {
		t.Fatal(err)
	}
	saved.on.Store(false) // goes away while the session is live
	same, err := a.ResumeSession(s.ID())
	if err != nil || same != s {
		t.Fatalf("the live handle must be returned untouched: %v", err)
	}
}

// A model that fits the new window is a plain resume; one that doesn't is
// compacted by the existing pre-turn check using the NEW model's window.
func TestFallbackSessionCompactsToFitTheNewModelsWindow(t *testing.T) {
	saved := newSwitchable("fb-saved", 1_000_000, "m1")
	small := newSwitchable("fb-small", 100, "tiny") // a much smaller window
	a, sm := fallbackEnv(t, saved, small)
	sm.SetActiveModel("fb-small/tiny")
	id := savedSession(t, a, "fb-saved/m1")

	// pretend the session had grown to 80% of the BIG window when it was saved
	meta, _, _ := a.store.LoadMeta(id)
	meta.Stats.ContextUsage = 0.8
	meta.Stats.ContextWindow = 1_000_000 // what a real session records at its last turn
	a.store.SaveMeta(meta)
	saved.on.Store(false)

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.contextWindow != 100 {
		t.Fatalf("context window = %d, want the NEW model's 100", s.contextWindow)
	}
	// The history holds what it held: 80% of the 1M window it was SAVED against
	// is 800k tokens — not 80 (80% of the new window). Against the small window
	// that is far over the threshold, so the pre-turn check compacts it.
	if s.lastInputTokens != 800_000 {
		t.Fatalf("lastInputTokens = %d, want 800000 (usage 0.8 × the SAVED 1M window)", s.lastInputTokens)
	}
	if s.stats.ContextWindow != 100 || s.stats.ContextUsage < autoCompactThreshold {
		t.Fatalf("stats window=%d usage=%v: must be re-expressed against the NEW window and read as over-threshold",
			s.stats.ContextWindow, s.stats.ContextUsage)
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("compaction on the fallback model: %v", err)
	}
	if small.calls.Load() != 1 {
		t.Errorf("the NEW model made %d compaction calls, want 1", small.calls.Load())
	}
}

// Fork has the same stranding bug and gets the same rule.
func TestForkFallsBackLikeResume(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	other := newSwitchable("fb-other", 1000, "o1")
	a, sm := fallbackEnv(t, saved, other)
	sm.SetActiveModel("fb-other/o1")
	id := savedSession(t, a, "fb-saved/m1")
	saved.on.Store(false)

	fork, err := a.ForkSession(id)
	if err != nil {
		t.Fatalf("fork must succeed through a fallback: %v", err)
	}
	if fork.Model() != "fb-other/o1" || fork.ModelFallback() == nil || fork.ModelFallback().From != "fb-saved/m1" {
		t.Errorf("fork model=%q fallback=%+v", fork.Model(), fork.ModelFallback())
	}
	if n := len(fork.AllMessages()); n != 2 {
		t.Errorf("the fork has %d messages, want the parent's 2", n)
	}
	// and the original session is untouched
	if m, _, _ := a.store.LoadMeta(id); m.Model != "fb-saved/m1" {
		t.Errorf("forking rewrote the PARENT's model to %q", m.Model)
	}
	// nothing to fall back to: fork refuses too
	other.on.Store(false)
	if _, err := a.ForkSession(id); !errors.Is(err, ErrNoModelAvailable) {
		t.Errorf("fork with nothing available: %v", err)
	}
}

// A malformed model id is not "unavailable": it does not trigger a fallback.
func TestOnlyUnavailabilityTriggersAFallback(t *testing.T) {
	other := newSwitchable("fb-other", 1000, "o1")
	a, sm := fallbackEnv(t, other)
	sm.SetActiveModel("fb-other/o1")
	if _, _, fb, err := resolveSessionModel("fb-other/o1"); err != nil || fb != nil {
		t.Errorf("a resolvable model: err=%v fb=%v", err, fb)
	}
	// an unknown PROVIDER is unavailability (it may have been removed)
	if _, _, fb, err := resolveSessionModel("ghost/x"); err != nil || fb == nil {
		t.Errorf("unknown provider: err=%v fb=%v, want a fallback", err, fb)
	}
	_ = a
	if providers.IsUnavailable(errors.New("boom")) {
		t.Error("an arbitrary error must not count as unavailability")
	}
}

// A plain resume (same model, same window) reconstructs the context exactly as
// before the fallback work: ratio × window.
func TestResumeWithoutFallbackRestoresContextUnchanged(t *testing.T) {
	p := newSwitchable("fb-saved", 200_000, "m1")
	a, _ := fallbackEnv(t, p)
	id := savedSession(t, a, "fb-saved/m1")
	meta, _, _ := a.store.LoadMeta(id)
	meta.Stats.ContextUsage = 0.4
	meta.Stats.ContextWindow = 200_000
	a.store.SaveMeta(meta)

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.lastInputTokens != 80_000 || s.stats.ContextUsage != 0.4 || s.stats.ContextWindow != 200_000 {
		t.Errorf("tokens=%d usage=%v window=%d, want 80000 / 0.4 / 200000 — a same-window resume must not change anything",
			s.lastInputTokens, s.stats.ContextUsage, s.stats.ContextWindow)
	}
}

// Sessions saved before Stats.ContextWindow was recorded have no saved window:
// the current one is used, which is what the code did before.
func TestResumeOfASessionWithNoRecordedWindowStillRestoresContext(t *testing.T) {
	p := newSwitchable("fb-saved", 200_000, "m1")
	a, _ := fallbackEnv(t, p)
	id := savedSession(t, a, "fb-saved/m1")
	meta, _, _ := a.store.LoadMeta(id)
	meta.Stats.ContextUsage = 0.5
	meta.Stats.ContextWindow = 0
	a.store.SaveMeta(meta)

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.lastInputTokens != 100_000 {
		t.Errorf("lastInputTokens = %d, want 100000 (0.5 × the current 200k window)", s.lastInputTokens)
	}
}

// Moving to a LARGER window lowers the pressure instead of leaving a stale high
// ratio that would trigger a pointless compaction.
func TestFallbackToALargerWindowLowersTheRecordedPressure(t *testing.T) {
	saved := newSwitchable("fb-saved", 200_000, "m1")
	big := newSwitchable("fb-big", 1_000_000, "b1")
	a, sm := fallbackEnv(t, saved, big)
	sm.SetActiveModel("fb-big/b1")
	id := savedSession(t, a, "fb-saved/m1")
	meta, _, _ := a.store.LoadMeta(id)
	meta.Stats.ContextUsage = 0.9 // 180k tokens of a 200k window
	meta.Stats.ContextWindow = 200_000
	a.store.SaveMeta(meta)
	saved.on.Store(false)

	s, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.lastInputTokens != 180_000 || s.stats.ContextWindow != 1_000_000 || s.stats.ContextUsage > 0.2 {
		t.Errorf("tokens=%d window=%d usage=%v, want 180000 tokens at ~18%% of the new 1M window",
			s.lastInputTokens, s.stats.ContextWindow, s.stats.ContextUsage)
	}
}

// "Unavailable" is the ONLY thing that triggers a fallback. Any other resolution
// failure must surface as it is: falling back on, say, a transient error would
// silently move a session to a different model for no reason. Resolve returns
// an error classified as neither unavailable nor fatal only through a provider
// whose Models() blows up — simulated here with a provider that lists nothing
// and whose model fetch errors out, which Resolve reports as "no models" (an
// unavailability) — so the non-unavailable path is pinned directly on the error
// classifier instead.
func TestResolveSessionModelReturnsOtherErrorsUntouched(t *testing.T) {
	other := newSwitchable("fb-other", 1000, "o1")
	_, sm := fallbackEnv(t, other)
	sm.SetActiveModel("fb-other/o1")

	// Every resolution failure Resolve itself produces is classified…
	for _, saved := range []string{"ghost/x", "fb-other/not-a-model"} {
		_, _, fb, err := resolveSessionModel(saved)
		if err != nil || fb == nil {
			t.Errorf("%s: err=%v fb=%v — an unavailable model must fall back", saved, err, fb)
		}
	}
	// …and the classifier says "no" to anything else, which is what keeps
	// resolveSessionModel from falling back on it.
	for _, e := range []error{errors.New("boom"), context.Canceled, context.DeadlineExceeded, errors.New("provider unreachable")} {
		if providers.IsUnavailable(e) {
			t.Errorf("%v must not be classified as unavailability", e)
		}
	}
	if !providers.IsUnavailable(errors.Join(errors.New("ctx"), providers.ErrNotActive)) {
		t.Error("a wrapped ErrNotActive must be recognised")
	}
}

// The "first available" step must never hand back the very model that just
// failed: with the session's provider still listed (but the model retired) and
// nothing else configured, the only candidate is a DIFFERENT model of that
// provider — never the retired id.
func TestFirstAvailableNeverReturnsTheModelThatFailed(t *testing.T) {
	p := newSwitchable("fb-one", 1000, "retired", "alive")
	fallbackEnv(t, p)
	// the model "retired" is still LISTED by the fake (so it resolves) — what
	// matters is that the helper skips whatever it is told to skip.
	if got := firstAvailableModel("fb-one/retired"); got != "fb-one/alive" {
		t.Errorf("firstAvailableModel skipping the failed one = %q, want fb-one/alive", got)
	}
	if got := firstAvailableModel("fb-one/alive"); got != "fb-one/retired" {
		t.Errorf("firstAvailableModel skipping alive = %q, want fb-one/retired", got)
	}
	// a provider that is off contributes nothing, even when it is first
	off := newSwitchable("fb-off", 1000, "x")
	off.on.Store(false)
	fallbackEnv(t, off, p)
	if got := firstAvailableModel(""); got != "fb-one/retired" {
		t.Errorf("an inactive provider must be skipped: got %q", got)
	}
	// nothing at all
	fallbackEnv(t, off)
	if got := firstAvailableModel(""); got != "" {
		t.Errorf("no available provider: got %q, want empty", got)
	}
}

// The rule that matters most to a user's data: only UNAVAILABILITY moves a session
// to another model. Any other resolution error is returned as it is, and the
// resolver is never asked for a fallback.
func TestOnlyUnavailabilityMovesASession(t *testing.T) {
	other := newSwitchable("fb-other", 1000, "o1")
	_, sm := fallbackEnv(t, other)
	sm.SetActiveModel("fb-other/o1")

	boom := errors.New("provider unreachable")
	var asked []string
	resolve := func(m string) (providers.Provider, string, error) {
		asked = append(asked, m)
		if m == "saved/x" {
			return nil, "", boom
		}
		return other, "o1", nil
	}
	_, _, fb, err := resolveSessionModelWith("saved/x", resolve)
	if !errors.Is(err, boom) || fb != nil {
		t.Errorf("a non-availability error: err=%v fb=%v, want it returned untouched with no fallback", err, fb)
	}
	if len(asked) != 1 {
		t.Errorf("the resolver was asked %v: no fallback candidate may be tried for a non-availability error", asked)
	}

	// the same saved model failing as UNAVAILABLE does move
	asked = nil
	unavailable := func(m string) (providers.Provider, string, error) {
		asked = append(asked, m)
		if m == "saved/x" {
			return nil, "", errors.Join(errors.New("gone"), providers.ErrNotActive)
		}
		return other, "o1", nil
	}
	if _, _, fb, err := resolveSessionModelWith("saved/x", unavailable); err != nil || fb == nil || fb.To != "fb-other/o1" {
		t.Errorf("an unavailable model: err=%v fb=%v", err, fb)
	}
}

// When the active model IS the model that just failed, it is not tried again.
func TestActiveModelEqualToTheFailedOneIsNotRetried(t *testing.T) {
	saved := newSwitchable("fb-saved", 1000, "m1")
	other := newSwitchable("fb-other", 1000, "o1")
	_, sm := fallbackEnv(t, saved, other)
	sm.SetActiveModel("fb-saved/m1")
	saved.on.Store(false)

	var asked []string
	resolve := func(m string) (providers.Provider, string, error) {
		asked = append(asked, m)
		return providers.Resolve(m)
	}
	_, _, fb, err := resolveSessionModelWith("fb-saved/m1", resolve)
	if err != nil || fb == nil || fb.To != "fb-other/o1" {
		t.Fatalf("err=%v fb=%v", err, fb)
	}
	for _, m := range asked[1:] {
		if m == "fb-saved/m1" {
			t.Errorf("the failed model was retried as a candidate: %v", asked)
		}
	}
}
