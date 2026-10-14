package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// errEmpty in a scriptedProvider's errs makes that call return a clean but
// EMPTY completion (no text/reasoning/tool call — the observed gateway flake)
// instead of an error.
var errEmpty = errors.New("scripted: empty completion")

// scriptedProvider fails its first len(errs) CompleteStream calls with errs
// (optionally after streaming some text first), then answers "done".
type scriptedProvider struct {
	fakeThinkingProbeProvider
	mu          sync.Mutex
	errs        []error
	streamFirst bool // emit a text delta before failing
	calls       int
}

func (p *scriptedProvider) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.mu.Unlock()
	if n < len(p.errs) {
		if p.errs[n] == errEmpty { // a clean, successful, EMPTY completion
			cb(types.StreamEvent{Type: types.StreamDone})
			return &types.Response{Message: types.NewAssistantToolCallMessage("", "", "", nil)}, nil
		}
		if p.streamFirst {
			cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "partial "})
		}
		return nil, p.errs[n]
	}
	cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "done"})
	cb(types.StreamEvent{Type: types.StreamDone})
	return &types.Response{Text: "done", Message: types.NewAssistantToolCallMessage("done", "", "", nil)}, nil
}

func (p *scriptedProvider) callCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

// retrySession runs one turn against a scripted provider with instant
// backoff, returning the provider, the session and every emitted event.
func retrySession(t *testing.T, p *scriptedProvider) (*Session, []types.Event, error) {
	t.Helper()
	withCleanProviderRegistry(t)
	oldBackoff := llmRetryBackoff
	llmRetryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { llmRetryBackoff = oldBackoff })

	var thinking string
	name := "retry-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	p.fakeThinkingProbeProvider = fakeThinkingProbeProvider{name: name, lastThinking: &thinking}
	providers.All = append(providers.All, p)

	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	t.Cleanup(func() { a.Close() })
	sess, err := a.NewSession(t.TempDir(), p.name+"/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []types.Event
	sess.Subscribe(func(e types.Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	_, turnErr := sess.PromptAndWait(context.Background(), "hi")
	mu.Lock()
	defer mu.Unlock()
	return sess, append([]types.Event(nil), events...), turnErr
}

func countType(evs []types.Event, typ types.EventType) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// The incident's error (a read that timed out on a dead connection) and the
// other transient shapes are retried; the turn completes and the history has
// exactly one clean assistant message.
func TestTransientErrorIsRetriedAndTurnCompletes(t *testing.T) {
	readTimeout := fmt.Errorf("http request: %w", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ETIMEDOUT})
	p := &scriptedProvider{errs: []error{readTimeout, fmt.Errorf("stream: %w", llm.ErrStreamIdle)}}
	sess, events, err := retrySession(t, p)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if p.callCount() != 3 {
		t.Errorf("provider called %d times, want 3", p.callCount())
	}
	if n := countType(events, types.EventRetry); n != 2 {
		t.Errorf("retry events = %d, want 2", n)
	}
	for _, e := range events {
		if e.Type == types.EventRetry && (e.MaxAttempts != llmMaxAttempts || e.Attempt < 2 || e.Message == "") {
			t.Errorf("bad retry event: %+v", e)
		}
	}
	if countType(events, types.EventError) != 0 {
		t.Error("a recovered turn must not emit an error")
	}
	var assistants []types.Message
	for _, m := range sess.AllMessages() {
		if m.Role == types.RoleAssistant {
			assistants = append(assistants, m)
		}
	}
	if len(assistants) != 1 || assistants[0].Parts[0].Text != "done" {
		t.Fatalf("history assistants = %+v, want exactly one 'done'", assistants)
	}
}

// After llmMaxAttempts transient failures, the turn fails with the last error.
func TestTransientErrorGivesUpAfterMaxAttempts(t *testing.T) {
	reset := fmt.Errorf("http request: %w", syscall.ECONNRESET)
	p := &scriptedProvider{errs: []error{reset, reset, reset, reset}}
	_, events, err := retrySession(t, p)
	if err == nil || !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("turn err = %v, want the connection reset", err)
	}
	if p.callCount() != llmMaxAttempts {
		t.Errorf("provider called %d times, want %d", p.callCount(), llmMaxAttempts)
	}
	if n := countType(events, types.EventRetry); n != llmMaxAttempts-1 {
		t.Errorf("retry events = %d, want %d", n, llmMaxAttempts-1)
	}
}

// 4xx and partially-streamed responses are NOT retried. (A context overflow
// isn't blindly retried either — it compacts first; see output_budget_test.go.)
func TestNonTransientErrorsAreNotRetried(t *testing.T) {
	cases := map[string]*scriptedProvider{
		"401":            {errs: []error{types.NewProviderAPIError("x", 401, []byte(`{"error":"unauthorized"}`))}},
		"403":            {errs: []error{types.NewProviderAPIError("x", 403, nil)}},
		"400":            {errs: []error{types.NewProviderAPIError("x", 400, []byte(`{"error":"bad request"}`))}},
		"partial stream": {errs: []error{fmt.Errorf("stream: %w", io.ErrUnexpectedEOF)}, streamFirst: true},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, events, err := retrySession(t, p)
			if err == nil {
				t.Fatal("the turn must fail")
			}
			if p.callCount() != 1 || countType(events, types.EventRetry) != 0 {
				t.Errorf("calls = %d, retries = %d; want 1, 0", p.callCount(), countType(events, types.EventRetry))
			}
		})
	}
}

// 502/503/504 are transient; the classifier table.
func TestIsTransientNetworkError(t *testing.T) {
	yes := []error{
		types.NewProviderAPIError("x", 502, nil), types.NewProviderAPIError("x", 503, nil), types.NewProviderAPIError("x", 504, nil),
		fmt.Errorf("x: %w", llm.ErrStreamIdle), io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE,
		errors.New(`Post "https://x": read tcp 1.2.3.4:5->6.7.8.9:443: read: connection timed out`),
		errors.New("net/http: timeout awaiting response headers"),
		errors.New("http2: server sent GOAWAY and closed the connection"),
		errors.New("stream connection lost: http2: client connection lost"),
	}
	no := []error{
		nil, context.Canceled, context.DeadlineExceeded, fmt.Errorf("wrapped: %w", context.Canceled),
		types.NewProviderAPIError("x", 400, nil), types.NewProviderAPIError("x", 401, nil),
		types.NewProviderAPIError("x", 429, nil), types.NewProviderAPIError("x", 500, nil),
		errors.New("prompt is too long"), errors.New("invalid api key"),
	}
	for _, e := range yes {
		if !isTransientNetworkError(e) {
			t.Errorf("want transient: %v", e)
		}
	}
	for _, e := range no {
		if isTransientNetworkError(e) {
			t.Errorf("want NOT transient: %v", e)
		}
	}
}

// A user Stop during the retry backoff ends the turn as a stop, not an error,
// and doesn't keep retrying.
func TestStopDuringRetryBackoff(t *testing.T) {
	withCleanProviderRegistry(t)
	oldBackoff := llmRetryBackoff
	llmRetryBackoff = func(int) time.Duration { return 5 * time.Second }
	t.Cleanup(func() { llmRetryBackoff = oldBackoff })

	var thinking string
	p := &scriptedProvider{errs: []error{syscall.ECONNRESET, syscall.ECONNRESET, syscall.ECONNRESET}}
	p.fakeThinkingProbeProvider = fakeThinkingProbeProvider{name: "retry-stop", lastThinking: &thinking}
	providers.All = append(providers.All, p)
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()
	sess, _ := a.NewSession(t.TempDir(), "retry-stop/fake-model")

	retried := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	sess.Subscribe(func(e types.Event) {
		switch e.Type {
		case types.EventRetry:
			retried <- struct{}{}
		case types.EventStop:
			stopped <- struct{}{}
		}
	})
	sess.Prompt(context.Background(), "hi")
	<-retried
	start := time.Now()
	sess.Stop()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop during the retry backoff didn't end the turn")
	}
	if time.Since(start) > time.Second || p.callCount() != 1 {
		t.Errorf("stop took %v, calls = %d; want prompt stop and no further attempts", time.Since(start), p.callCount())
	}
}

// Criterion 1: an empty completion is retried; the retry's content completes
// the turn and nothing empty is stored.
func TestEmptyResponseIsRetried(t *testing.T) {
	p := &scriptedProvider{errs: []error{errEmpty}}
	sess, events, err := retrySession(t, p)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if p.callCount() != 2 || countType(events, types.EventRetry) != 1 {
		t.Errorf("calls = %d, retries = %d; want 2, 1", p.callCount(), countType(events, types.EventRetry))
	}
	for _, m := range sess.AllMessages() {
		if m.Role == types.RoleAssistant && !m.HasContent() {
			t.Fatal("an empty assistant message was persisted")
		}
	}
}

// Criterion 2: three empty completions → an error event, the turn ends, and
// nothing empty is stored.
func TestEmptyResponsesExhaustToError(t *testing.T) {
	p := &scriptedProvider{errs: []error{errEmpty, errEmpty, errEmpty}}
	sess, events, err := retrySession(t, p)
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("turn err = %v, want ErrEmptyResponse", err)
	}
	var sawError bool
	for _, e := range events {
		if e.Type == types.EventError && strings.Contains(e.Message, "empty response") {
			sawError = true
		}
	}
	if !sawError {
		t.Error("an error event must report the empty response")
	}
	if p.callCount() != llmMaxAttempts {
		t.Errorf("calls = %d, want %d", p.callCount(), llmMaxAttempts)
	}
	for _, m := range sess.AllMessages() {
		if m.Role == types.RoleAssistant {
			t.Fatalf("nothing must be stored for an empty turn, got %+v", m)
		}
	}
}

// The max-iterations progress summary never persists an empty assistant
// message either: empty summaries surface as an error and nothing is stored.
func TestEmptyProgressSummaryIsNotStored(t *testing.T) {
	p := &scriptedProvider{} // first turn answers "done"
	sess, _, err := retrySession(t, p)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.errs = make([]error, 100) // from here on: every call is an empty completion
	for i := range p.errs {
		p.errs[i] = errEmpty
	}
	p.calls = 0
	p.mu.Unlock()

	before := len(sess.AllMessages())
	sess.mu.Lock() // requestProgressUpdate runs under the turn lock in production
	_, err = sess.requestProgressUpdate(context.Background())
	sess.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want an empty-summary error", err)
	}
	for _, m := range sess.AllMessages()[before:] {
		if m.Role == types.RoleAssistant {
			t.Fatalf("an empty summary was persisted: %+v", m)
		}
	}
}
