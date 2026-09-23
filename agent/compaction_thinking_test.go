package agent

import (
	"context"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// fakeThinkingProbeProvider is a minimal providers.Provider stub whose only
// real job is CompleteStream: it records the ThinkingLevel of every request
// it receives and returns a fixed, valid-looking response so the caller
// (generateCompactionSummary) completes normally. Registered directly into
// the global providers.All (snapshotted/restored via withCleanProviderRegistry,
// same isolation pattern agent/custom_providers_test.go already uses) rather
// than going through RegisterOpenAI, since this needs to be a fully custom
// Provider implementation, not an OpenAI-dialect one.
type fakeThinkingProbeProvider struct {
	name           string
	lastThinking   *string // set by CompleteStream on every call
	thinkingLegacy bool
}

func (f *fakeThinkingProbeProvider) CompleteStream(_ context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	*f.lastThinking = req.ThinkingLevel
	if cb != nil {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "ok"})
	}
	return &types.Response{Text: "ok", Message: types.NewAssistantToolCallMessage("ok", "", "", nil)}, nil
}
func (f *fakeThinkingProbeProvider) Name() string        { return f.name }
func (f *fakeThinkingProbeProvider) DisplayName() string { return f.name }
func (f *fakeThinkingProbeProvider) Description() string { return "fake" }
func (f *fakeThinkingProbeProvider) IsActive() bool      { return true }
func (f *fakeThinkingProbeProvider) Models() []types.ModelMeta {
	return []types.ModelMeta{{ID: "fake-model", ContextWindow: 128000}}
}
func (f *fakeThinkingProbeProvider) FetchModels() ([]types.ModelMeta, error) { return f.Models(), nil }
func (f *fakeThinkingProbeProvider) ModelMeta(modelID string) *types.ModelMeta {
	return &types.ModelMeta{ID: modelID, ContextWindow: 128000}
}
func (f *fakeThinkingProbeProvider) CredentialType() types.CredentialType { return types.CredTypeNone }
func (f *fakeThinkingProbeProvider) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: types.CredTypeNone}, nil
}
func (f *fakeThinkingProbeProvider) ActivationSource() providers.ActivationSource {
	return providers.ActivationAuto
}
func (f *fakeThinkingProbeProvider) Connect(types.Credentials) error { return nil }
func (f *fakeThinkingProbeProvider) Disconnect() error               { return nil }

// TestCompactionSummaryUsesSessionThinkingLevel is the regression test for
// a real bug: generateCompactionSummary's own *types.Request never set
// ThinkingLevel at all (a plain oversight since the feature was first
// introduced, not a deliberate "summaries don't need thinking" decision) —
// so a session configured with e.g. "high" thinking silently ran its
// compaction summary call with NO thinking level at all (interpreted
// downstream as "off"), instead of honoring what the session — and every
// REGULAR turn — actually uses. Confirmed live this also caused a hard
// failure against adaptive-only models (opus-5 family), which reject the
// wire shape an empty ThinkingLevel used to produce.
func TestCompactionSummaryUsesSessionThinkingLevel(t *testing.T) {
	withCleanProviderRegistry(t)

	var lastThinking string
	fake := &fakeThinkingProbeProvider{name: "fake-thinking-probe", lastThinking: &lastThinking}
	providers.All = append(providers.All, fake)

	a := New(AgentOptions{Store: store.NewInMemoryStore(), ThinkingLevel: "high"})
	defer a.Close()

	sess, err := a.NewSession(t.TempDir(), "fake-thinking-probe/fake-model")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if got := sess.Thinking(); got != "high" {
		t.Fatalf("session thinking = %q, want high", got)
	}

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if lastThinking != "high" {
		t.Errorf("compaction request's ThinkingLevel = %q, want %q (the session's own configured level)", lastThinking, "high")
	}
}

// TestCompactionSummaryUsesUpdatedThinkingLevelAfterSwitch confirms the fix
// reads the CURRENT thinking level at compaction time, not whatever was
// configured when the session was first built — same reasoning
// SwitchThinking's own contract already guarantees for regular turns.
func TestCompactionSummaryUsesUpdatedThinkingLevelAfterSwitch(t *testing.T) {
	withCleanProviderRegistry(t)

	var lastThinking string
	fake := &fakeThinkingProbeProvider{name: "fake-thinking-probe-2", lastThinking: &lastThinking}
	providers.All = append(providers.All, fake)

	a := New(AgentOptions{Store: store.NewInMemoryStore(), ThinkingLevel: "low"})
	defer a.Close()

	sess, err := a.NewSession(t.TempDir(), "fake-thinking-probe-2/fake-model")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.SwitchThinking("xhigh"); err != nil {
		t.Fatalf("SwitchThinking: %v", err)
	}

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if lastThinking != "xhigh" {
		t.Errorf("compaction request's ThinkingLevel = %q, want %q (the level AFTER SwitchThinking, not the session's original default)", lastThinking, "xhigh")
	}
}
