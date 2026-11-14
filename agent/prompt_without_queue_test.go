package agent

import (
	"context"
	"errors"
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

// On an IDLE session PromptWithoutQueue is an ordinary prompt: it starts at once.
func TestPromptWithoutQueueStartsOnAnIdleSession(t *testing.T) {
	sess, p := newGatedSession(t)
	close(p.gate)
	text, err := sess.PromptAndWait(context.Background(), "hello", PromptWithoutQueue())
	if err != nil || text != "ok" {
		t.Fatalf("idle session: text=%q err=%v", text, err)
	}
	if got := sess.Prompt(context.Background(), "again", PromptWithoutQueue()); got != types.PromptStarted {
		t.Errorf("Prompt on an idle session = %v, want PromptStarted", got)
	}
}

// On a BUSY session it is refused — and nothing is queued, stored or run.
func TestPromptWithoutQueueIsRefusedWhileBusyAndLeavesNoTrace(t *testing.T) {
	sess, p := newGatedSession(t)
	if sess.Prompt(context.Background(), "the user's long task") != types.PromptStarted {
		t.Fatal("setup: the first prompt must start")
	}
	<-p.started // the turn is genuinely in flight
	if !sess.IsBusy() {
		t.Fatal("setup: the session must be busy")
	}

	if got := sess.Prompt(context.Background(), "colleague asks", PromptWithoutQueue(), PromptWithOrigin("colleague")); got != types.PromptRejected {
		t.Errorf("Prompt while busy = %v, want PromptRejected", got)
	}
	start := time.Now()
	_, err, returned := promptAndWaitWithin(t, sess, 2*time.Second, "colleague asks again", PromptWithoutQueue())
	if !returned {
		close(p.gate) // unblock the stuck turn so the test can end
		t.Fatal("PromptAndWait while busy did not return in 2s: it was queued behind the running turn instead of being refused")
	}
	if !errors.Is(err, ErrBusy) {
		t.Errorf("PromptAndWait while busy: err = %v, want ErrBusy", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("the refusal took %v: it must be immediate", time.Since(start))
	}
	if n := sess.FollowUpCount(); n != 0 {
		t.Errorf("queue depth = %d: a refused prompt must not be queued", n)
	}

	close(p.gate) // let the user's turn finish
	if _, err := sess.PromptAndWait(context.Background(), "after"); err != nil {
		t.Fatal(err)
	}
	for _, txt := range texts(sess) {
		if strings.Contains(txt, "colleague asks") {
			t.Fatalf("a refused prompt reached the conversation: %v", texts(sess))
		}
	}
	if p.calls != 2 {
		t.Errorf("the model ran %d turns, want 2 (the user's, and 'after') — never one for a refused prompt", p.calls)
	}
}

// The default is unchanged: without the option a busy session QUEUES, in order.
func TestPromptWithoutTheOptionStillQueues(t *testing.T) {
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

// The check and the enqueue are ONE step: when many no-queue prompts race on an
// idle session, exactly one starts and every other is refused — none slips in
// behind it (that would be the queueing this option exists to prevent).
func TestPromptWithoutQueueRaceExactlyOneWins(t *testing.T) {
	sess, p := newGatedSession(t)
	const racers = 40
	results := make(chan types.PromptStatus, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- sess.Prompt(context.Background(), "race", PromptWithoutQueue())
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var started, rejected, queued int
	for r := range results {
		switch r {
		case types.PromptStarted:
			started++
		case types.PromptRejected:
			rejected++
		case types.PromptQueued:
			queued++
		}
	}
	if started != 1 || rejected != racers-1 || queued != 0 {
		t.Errorf("started=%d rejected=%d queued=%d, want 1 / %d / 0", started, rejected, queued, racers-1)
	}
	close(p.gate)
}
