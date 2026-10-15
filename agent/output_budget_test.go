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

// ── Pure budget rules ─────────────────────────────────────────────────────

func TestSessionMaxTokensPrecedence(t *testing.T) {
	cat := func(n int) *types.ModelMeta { return &types.ModelMeta{MaxTokens: n} }
	cases := []struct {
		name     string
		explicit int
		meta     *types.ModelMeta
		want     int
	}{
		{"catalog above the default cap is capped", 0, cat(512000), defaultMaxOutputTokens},
		{"catalog below the cap is kept", 0, cat(8000), 8000},
		{"unknown model falls back", 0, nil, fallbackMaxOutputTokens},
		{"explicit wins over a bigger catalog and the cap", 32000, cat(512000), 32000},
		{"explicit above the default cap is respected", 256000, cat(512000), 256000},
		{"explicit tiny is respected", 8000, cat(512000), 8000},
	}
	for _, c := range cases {
		if got := sessionMaxTokens(c.explicit, c.meta); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestRequestMaxTokensBounds(t *testing.T) {
	const window = 1_000_000
	cases := []struct {
		name                 string
		ceiling, input, want int
	}{
		{"small session keeps the ceiling", 64000, 100_000, 64000},
		{"user 256k with room keeps it", 256000, 200_000, 256000},
		{"near the window it's clamped to the room", 256000, 900_000, 50_000},
		{"never below the floor", 64000, 990_000, minOutputTokens},
		{"a ceiling below the floor wins", 8000, 990_000, 8000},
		{"never above the ceiling", 32000, 0, 32000},
	}
	for _, c := range cases {
		got := requestMaxTokens(c.ceiling, window, c.input)
		if got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
		if got > c.ceiling {
			t.Errorf("%s: %d exceeds the ceiling %d", c.name, got, c.ceiling)
		}
	}
}

// Criterion 3 (rule level): reserved output counts — the new rule compacts at
// an input level where the old input-only rule stayed silent.
func TestNeedsCompactionCountsReservedOutput(t *testing.T) {
	const window = 1_000_000
	input := 940_000 // 94%: below the old 95% input-only trigger
	if float64(input)/float64(window) >= autoCompactThreshold {
		t.Fatal("precondition: the old rule must not fire at this input")
	}
	if !needsCompaction(window, input, 64000) {
		t.Error("input + reserved output crosses the threshold: must compact")
	}
	if needsCompaction(window, 500_000, 64000) {
		t.Error("half-full session must not compact")
	}
}

// ── MiniMax-style provider ────────────────────────────────────────────────

// windowProvider mimics MiniMax: it REJECTS a request when input + max_tokens
// exceeds its window, with MiniMax's real error text. "Input" is the request's
// size in chars / charsPerToken, and it reports that as usage, so the session
// tracks it like a real provider. overflowFirst makes the first call overflow
// regardless (a tokenizer disagreement our estimate couldn't foresee).
type windowProvider struct {
	fakeThinkingProbeProvider
	window         int
	charsPerToken  int
	overflowFirst  int // number of leading calls that overflow regardless
	alwaysOverflow bool
	gate           chan struct{} // if set, the first compaction call blocks on it

	mu        sync.Mutex
	calls     int
	maxTokens []int // max_tokens of every main (tools != nil) request
	compacts  int
}

func (p *windowProvider) input(req *types.Request) int {
	return (len(req.SystemPrompt) + approxSize(req.Messages)) / p.charsPerToken
}

func (p *windowProvider) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	isCompaction := req.SystemPrompt == compactSystemPrompt
	if isCompaction {
		p.compacts++
	} else {
		p.maxTokens = append(p.maxTokens, req.MaxTokens)
	}
	gate := p.gate
	p.mu.Unlock()

	in := p.input(req)
	if isCompaction {
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "compact summary"})
		return &types.Response{Text: "compact summary", Message: types.NewAssistantToolCallMessage("compact summary", "", "", nil)}, nil
	}
	if p.alwaysOverflow || n < p.overflowFirst || in+req.MaxTokens > p.window {
		return nil, errors.New("minimax API error 400: invalid params, context window exceeds limit (2013)")
	}
	cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "ok"})
	cb(types.StreamEvent{Type: types.StreamUsage, InputTokens: in})
	return &types.Response{Text: "ok", Message: types.NewAssistantToolCallMessage("ok", "", "", nil)}, nil
}

func (p *windowProvider) Models() []types.ModelMeta {
	return []types.ModelMeta{{ID: "fake-model", ContextWindow: p.window, MaxTokens: 512000}}
}
func (p *windowProvider) ModelMeta(modelID string) *types.ModelMeta {
	return &types.ModelMeta{ID: modelID, ContextWindow: p.window, MaxTokens: 512000} // catalog says 512k, like MiniMax-M3
}

// windowSession: a session on p whose history is pre-filled to inputTokens.
func windowSession(t *testing.T, p *windowProvider, opts AgentOptions, inputTokens int) *Session {
	t.Helper()
	withCleanProviderRegistry(t)
	oldBackoff := llmRetryBackoff
	llmRetryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { llmRetryBackoff = oldBackoff })

	var thinking string
	p.fakeThinkingProbeProvider = fakeThinkingProbeProvider{name: "window-" + strings.NewReplacer("/", "-").Replace(t.Name()), lastThinking: &thinking}
	providers.AddForTest(p)
	opts.Store = store.NewInMemoryStore()
	a := New(opts)
	t.Cleanup(func() { a.Close() })
	sess, err := a.NewSession(t.TempDir(), p.name+"/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	if inputTokens > 0 {
		filler := strings.Repeat("x", inputTokens*p.charsPerToken)
		sess.store.AddMessage(types.NewUserTextMessage(filler))
		sess.store.AddMessage(types.NewAssistantToolCallMessage("noted", "", "", nil))
		sess.mu.Lock()
		sess.lastInputTokens = inputTokens
		sess.mu.Unlock()
	}
	return sess
}

func runTurn(t *testing.T, sess *Session) ([]types.Event, error) {
	t.Helper()
	var mu sync.Mutex
	var evs []types.Event
	sess.Subscribe(func(e types.Event) { mu.Lock(); evs = append(evs, e); mu.Unlock() })
	_, err := sess.PromptAndWait(context.Background(), "go on")
	mu.Lock()
	defer mu.Unlock()
	return append([]types.Event(nil), evs...), err
}

// Criterion 1 — the incident: catalog says 512k, input is ~60% of a 1M window
// (sum > window with 512k). The request's max_tokens is bounded, the provider
// accepts it, no 400.
func TestLargeSessionGetsBoundedMaxTokens(t *testing.T) {
	p := &windowProvider{window: 1_000_000, charsPerToken: 2}
	sess := windowSession(t, p, AgentOptions{}, 600_000)
	evs, err := runTurn(t, sess)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if countType(evs, types.EventRetry) != 0 || p.compacts != 0 {
		t.Errorf("retries=%d compacts=%d; want a clean first-try success", countType(evs, types.EventRetry), p.compacts)
	}
	got := p.maxTokens[0]
	if got > defaultMaxOutputTokens || got < minOutputTokens {
		t.Errorf("max_tokens = %d, want within [%d, %d]", got, minOutputTokens, defaultMaxOutputTokens)
	}
}

// Criterion 2 — explicit AgentOptions.MaxTokens wins over catalog and cap; a
// bigger explicit value is still clamped by the window when needed.
func TestExplicitMaxTokensWins(t *testing.T) {
	p := &windowProvider{window: 1_000_000, charsPerToken: 2}
	sess := windowSession(t, p, AgentOptions{MaxTokens: 32000}, 100_000)
	if _, err := runTurn(t, sess); err != nil {
		t.Fatal(err)
	}
	if p.maxTokens[0] != 32000 {
		t.Errorf("max_tokens = %d, want the explicit 32000", p.maxTokens[0])
	}
}

func TestExplicitBigMaxTokensIsWindowBounded(t *testing.T) {
	p2 := &windowProvider{window: 1_000_000, charsPerToken: 2}
	sess2 := windowSession(t, p2, AgentOptions{MaxTokens: 256000}, 850_000)
	if _, err := runTurn(t, sess2); err != nil {
		t.Fatalf("explicit 256k on a large session: %v", err)
	}
	if p2.maxTokens[0] >= 256000 || p2.maxTokens[0] < minOutputTokens {
		t.Errorf("max_tokens = %d, want the explicit 256k clamped by the window", p2.maxTokens[0])
	}
}

// Criterion 6 — small sessions keep today's value (the capped ceiling).
func TestSmallSessionUnchanged(t *testing.T) {
	p := &windowProvider{window: 1_000_000, charsPerToken: 2}
	sess := windowSession(t, p, AgentOptions{}, 0)
	if _, err := runTurn(t, sess); err != nil {
		t.Fatal(err)
	}
	if p.maxTokens[0] != defaultMaxOutputTokens || p.compacts != 0 {
		t.Errorf("max_tokens = %d, compacts = %d; want %d, 0", p.maxTokens[0], p.compacts, defaultMaxOutputTokens)
	}
}

// Criterion 3 — a session at 94% of the window (old input-only rule silent)
// compacts BEFORE its request, because input + reserved output crosses it.
func TestAutoCompactCountsReservedOutput(t *testing.T) {
	p := &windowProvider{window: 1_000_000, charsPerToken: 2}
	sess := windowSession(t, p, AgentOptions{}, 940_000)
	evs, err := runTurn(t, sess)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if p.compacts != 1 || countType(evs, types.EventCompactStart) != 1 {
		t.Errorf("compacts = %d; want exactly one, before the request", p.compacts)
	}
	if countType(evs, types.EventRetry) != 0 {
		t.Error("it must compact up front, not after a refused request")
	}
}

// Criterion 4 — the provider refuses with MiniMax's (and Anthropic's/OpenAI's)
// overflow text on the first attempt: the session compacts ONCE, retries with
// a retry event, and the turn completes.
func TestContextOverflowCompactsAndRetries(t *testing.T) {
	for name, msg := range map[string]string{
		"minimax":   "minimax API error 400: invalid params, context window exceeds limit (2013)",
		"anthropic": "prompt is too long: 210000 tokens > 200000 maximum",
		"openai":    "This model's maximum context length is 128000 tokens",
	} {
		t.Run(name, func(t *testing.T) {
			p := &overflowOnceProvider{msg: msg}
			sess := windowSession(t, &p.windowProvider, AgentOptions{}, 0)
			// Re-register the wrapper so its CompleteStream is the one used.
			p.name = p.windowProvider.name
			list := providers.All()
			repl := append([]providers.Provider{}, list[:len(list)-1]...)
			providers.SetForTest(append(repl, p))
			sess.provider = p
			evs, err := runTurn(t, sess)
			if err != nil {
				t.Fatalf("turn failed: %v", err)
			}
			if p.compacts != 1 || countType(evs, types.EventRetry) != 1 {
				t.Errorf("compacts=%d retries=%d; want 1, 1", p.compacts, countType(evs, types.EventRetry))
			}
		})
	}
}

// overflowOnceProvider fails the first MAIN request with msg, then behaves.
type overflowOnceProvider struct {
	windowProvider
	msg  string
	done bool
}

func (p *overflowOnceProvider) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	if req.SystemPrompt != compactSystemPrompt && !p.done {
		p.done = true
		p.mu.Lock()
		p.calls++
		p.mu.Unlock()
		return nil, errors.New(p.msg)
	}
	if p.window == 0 {
		p.window, p.charsPerToken = 1_000_000, 2
	}
	return p.windowProvider.CompleteStream(ctx, req, cb)
}

// Criterion 5 — if the retry overflows again, the turn ends with an error and
// nothing loops (exactly one compaction).
func TestContextOverflowTwiceIsFinal(t *testing.T) {
	p := &windowProvider{window: 1_000_000, charsPerToken: 2, alwaysOverflow: true}
	sess := windowSession(t, p, AgentOptions{}, 0)
	evs, err := runTurn(t, sess)
	if err == nil || !isContextOverflowError(err) {
		t.Fatalf("turn err = %v, want the overflow", err)
	}
	if p.compacts != 1 {
		t.Errorf("compacts = %d, want exactly 1 (no loop)", p.compacts)
	}
	if countType(evs, types.EventError) == 0 {
		t.Error("an error event must end the turn")
	}
}

// Criterion 5 — a user Stop during the compact-and-retry is honoured.
func TestStopDuringOverflowCompaction(t *testing.T) {
	p := &windowProvider{window: 1_000_000, charsPerToken: 2, overflowFirst: 1, gate: make(chan struct{})}
	t.Cleanup(func() { close(p.gate) })
	sess := windowSession(t, p, AgentOptions{}, 0)

	compacting := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	sess.Subscribe(func(e types.Event) {
		switch e.Type {
		case types.EventCompactStart:
			compacting <- struct{}{}
		case types.EventStop:
			stopped <- struct{}{}
		}
	})
	sess.Prompt(context.Background(), "go")
	select {
	case <-compacting:
	case <-time.After(3 * time.Second):
		t.Fatal("never started compacting")
	}
	sess.Stop()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop during the overflow compaction wasn't honoured")
	}
}
