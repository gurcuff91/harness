package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// gatedProvider answers every call with "ok", but blocks each one on gate until it
// is closed — a turn that stays in flight for as long as the test wants.
type gatedProvider struct {
	fakeThinkingProbeProvider
	gate    chan struct{}
	started chan struct{} // receives one value per call that begins
	mu      sync.Mutex
	calls   int
}

func (p *gatedProvider) CompleteStream(ctx context.Context, _ *types.Request, cb types.StreamCallback) (*types.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "ok"})
	cb(types.StreamEvent{Type: types.StreamDone})
	return &types.Response{Text: "ok", Message: types.NewAssistantToolCallMessage("ok", "", "", nil)}, nil
}

func newGatedSession(t *testing.T) (*Session, *gatedProvider) {
	t.Helper()
	withCleanProviderRegistry(t)
	var thinking string
	name := "gate-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	p := &gatedProvider{gate: make(chan struct{}), started: make(chan struct{}, 8)}
	p.fakeThinkingProbeProvider = fakeThinkingProbeProvider{name: name, lastThinking: &thinking}
	providers.AddForTest(p)
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	t.Cleanup(func() { a.Close() })
	sess, err := a.NewSession(t.TempDir(), name+"/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	return sess, p
}

// promptAndWaitWithin runs PromptAndWait with a deadline so a prompt that should have
// been refused but is instead queued behind the running turn FAILS the test within
// seconds, instead of blocking it until go test's own timeout.
func promptAndWaitWithin(t *testing.T, s *Session, d time.Duration, text string, opts ...PromptOption) (string, error, bool) {
	t.Helper()
	type res struct {
		text string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		tx, err := s.PromptAndWait(context.Background(), text, opts...)
		ch <- res{tx, err}
	}()
	select {
	case r := <-ch:
		return r.text, r.err, true
	case <-time.After(d):
		return "", nil, false
	}
}

func texts(s *Session) []string {
	var out []string
	for _, m := range s.AllMessages() {
		if m.Role == types.RoleUser && len(m.Parts) > 0 {
			out = append(out, m.Parts[0].Text)
		}
	}
	return out
}

// A prompt to a busy session is QUEUED and runs after, in order.
func TestABusySessionQueuesPrompts(t *testing.T) {
	sess, p := newGatedSession(t)
	sess.Prompt(context.Background(), "first")
	<-p.started
	if got := sess.Prompt(context.Background(), "second"); got != types.PromptQueued {
		t.Errorf("a plain prompt on a busy session = %v, want PromptQueued", got)
	}
	if sess.FollowUpCount() != 1 {
		t.Errorf("queue depth = %d, want 1", sess.FollowUpCount())
	}
	close(p.gate)
	deadline := time.Now().Add(5 * time.Second)
	for sess.IsBusy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := texts(sess); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("history = %v, want both prompts in order", got)
	}
}
