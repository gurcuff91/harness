package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// reply is one scripted model response, streamed the way a real provider does.
type reply struct {
	thinking string // reasoning, streamed as thinking deltas
	text     string
	tool     string // a tool call to this tool, when non-empty
	empty    bool   // a clean completion carrying nothing at all
}

// scriptProvider answers call n with script[n] (the last entry repeats).
type scriptProvider struct {
	fakeThinkingProbeProvider
	mu     sync.Mutex
	script []reply
	calls  int
}

func (p *scriptProvider) CompleteStream(_ context.Context, _ *types.Request, cb types.StreamCallback) (*types.Response, error) {
	p.mu.Lock()
	r := p.script[min(p.calls, len(p.script)-1)]
	p.calls++
	p.mu.Unlock()

	var calls []types.ToolCall
	if r.thinking != "" {
		cb(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: r.thinking})
	}
	if r.text != "" {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: r.text})
	}
	if r.tool != "" {
		args := json.RawMessage(`{}`)
		cb(types.StreamEvent{Type: types.StreamToolStart, ToolID: "t1", ToolName: r.tool})
		cb(types.StreamEvent{Type: types.StreamToolEnd, ToolID: "t1", ToolName: r.tool, ToolArgs: args})
		calls = append(calls, types.ToolCall{ID: "t1", Name: r.tool, Input: args})
	}
	cb(types.StreamEvent{Type: types.StreamUsage, InputTokens: 10, OutputTokens: 5})
	cb(types.StreamEvent{Type: types.StreamDone})
	msg := types.NewAssistantToolCallMessage(r.text, r.thinking, "", calls)
	return &types.Response{Text: r.text, Message: msg, ToolCalls: calls}, nil
}
func (p *scriptProvider) callCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

// runScripted runs ONE turn against the script and returns the session, every
// event, the turn's error and the provider.
func runScripted(t *testing.T, script ...reply) (*Session, []types.Event, error, *scriptProvider) {
	t.Helper()
	withCleanProviderRegistry(t)
	var thinking string
	name := "ro-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	p := &scriptProvider{script: script}
	p.fakeThinkingProbeProvider = fakeThinkingProbeProvider{name: name, lastThinking: &thinking}
	providers.AddForTest(p)

	a := New(AgentOptions{Store: store.NewInMemoryStore(), Tools: []tools.Tool{{
		Def:     types.ToolDef{Name: "Probe", Description: "probe", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Execute: func(context.Context, json.RawMessage) (string, error) { return "probe-ok", nil },
	}}})
	t.Cleanup(func() { a.Close() })
	sess, err := a.NewSession(t.TempDir(), name+"/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []types.Event
	sess.Subscribe(func(e types.Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	_, turnErr := sess.PromptAndWait(context.Background(), "do the task")
	mu.Lock()
	defer mu.Unlock()
	return sess, append([]types.Event(nil), events...), turnErr, p
}

func assistantMessages(s *Session) []types.Message {
	var out []types.Message
	for _, m := range s.AllMessages() {
		if m.Role == types.RoleAssistant {
			out = append(out, m)
		}
	}
	return out
}

// 1 + 2 + 4: a reasoning-only iteration ends the turn with an error carrying the
// stable message; loop_end and turn_end are still emitted; nothing is persisted
// (no assistant message, so no reasoning replayed later); and harness does not
// retry on its own — the provider is called once.
func TestReasoningOnlyEndsTheTurnWithAnError(t *testing.T) {
	sess, events, err, p := runScripted(t, reply{thinking: strings.Repeat("hmm: ", 5000)})

	if !errors.Is(err, ErrReasoningOnly) {
		t.Fatalf("turn err = %v, want ErrReasoningOnly", err)
	}
	var errEvents []types.Event
	for _, e := range events {
		if e.Type == types.EventError {
			errEvents = append(errEvents, e)
		}
	}
	if len(errEvents) != 1 || errEvents[0].Message != "model returned reasoning but no answer" {
		t.Fatalf("error events = %+v, want exactly one with the stable message", errEvents)
	}
	if countType(events, types.EventLoopStart) != 1 || countType(events, types.EventLoopEnd) != 1 || countType(events, types.EventTurnEnd) != 1 {
		t.Errorf("loop_start/loop_end/turn_end = %d/%d/%d, want 1/1/1 (balanced, as for any provider error)",
			countType(events, types.EventLoopStart), countType(events, types.EventLoopEnd), countType(events, types.EventTurnEnd))
	}
	if n := p.callCount(); n != 1 {
		t.Errorf("provider called %d times: no silent retry once reasoning has streamed", n)
	}
	if n := countType(events, types.EventRetry); n != 0 {
		t.Errorf("%d retry events, want none", n)
	}
	if got := assistantMessages(sess); len(got) != 0 {
		t.Fatalf("the reasoning-only message was persisted: %d assistant messages", len(got))
	}
	// the reasoning itself did stream to the client (that is why no retry is safe)
	if countType(events, types.EventStreamThinkingDelta) == 0 {
		t.Error("the reasoning should have streamed to the client")
	}
}

// 2 (replay): the next turn on the same session does not carry the degenerate
// reasoning — the history sent to the model holds only the user's prompts.
func TestReasoningOnlyIsNotReplayedInLaterRequests(t *testing.T) {
	sess, _, err, _ := runScripted(t, reply{thinking: "hmm: hmm: hmm:"}, reply{text: "ok now"})
	if !errors.Is(err, ErrReasoningOnly) {
		t.Fatal(err)
	}
	if _, err := sess.PromptAndWait(context.Background(), "continua"); err != nil {
		t.Fatalf("the follow-up turn must work: %v", err)
	}
	for _, m := range sess.AllMessages() {
		for _, part := range m.Parts {
			if part.Thinking != nil && strings.Contains(part.Thinking.Content, "hmm") {
				t.Fatal("the degenerate reasoning is in the history, so it would be replayed")
			}
		}
	}
	got := assistantMessages(sess)
	if len(got) != 1 || got[0].Parts[0].Text != "ok now" {
		t.Errorf("assistant history = %+v, want only the follow-up answer", got)
	}
}

// 3: reasoning + an answer is a normal reply — exactly as before: persisted with
// both parts, turn completes, no error.
func TestReasoningWithTextIsUnchanged(t *testing.T) {
	sess, events, err, _ := runScripted(t, reply{thinking: "let me think", text: "the answer"})
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if countType(events, types.EventError) != 0 {
		t.Error("no error event for reasoning + text")
	}
	got := assistantMessages(sess)
	if len(got) != 1 || len(got[0].Parts) != 2 || got[0].Parts[0].Thinking == nil || got[0].Parts[1].Text != "the answer" {
		t.Fatalf("persisted = %+v, want thinking + text", got)
	}
}

// 3: reasoning + a tool call continues the loop: the tool runs, the next
// iteration answers, and the turn completes. Nothing about it changed.
func TestReasoningWithToolCallIsUnchanged(t *testing.T) {
	sess, events, err, p := runScripted(t,
		reply{thinking: "I should probe", tool: "Probe"},
		reply{thinking: "done probing", text: "finished"},
	)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if p.callCount() != 2 || countType(events, types.EventError) != 0 {
		t.Errorf("calls=%d errors=%d, want 2 iterations and no error", p.callCount(), countType(events, types.EventError))
	}
	if countType(events, types.EventToolResult) != 1 {
		t.Errorf("tool results = %d, want 1", countType(events, types.EventToolResult))
	}
	if got := assistantMessages(sess); len(got) != 2 {
		t.Errorf("assistant messages = %d, want 2 (tool iteration + final answer)", len(got))
	}
}

// 3: a tool call with NO reasoning, and plain text with no reasoning, are likewise untouched.
func TestPlainTextAndBareToolCallAreUnchanged(t *testing.T) {
	t.Run("plain text", func(t *testing.T) {
		if _, _, err, _ := runScripted(t, reply{text: "hello"}); err != nil {
			t.Errorf("plain text: %v", err)
		}
	})
	t.Run("bare tool call", func(t *testing.T) {
		_, _, err, p := runScripted(t, reply{tool: "Probe"}, reply{text: "ok"})
		if err != nil || p.callCount() != 2 {
			t.Errorf("bare tool call: err=%v calls=%d", err, p.callCount())
		}
	})
}

// 4 + 5: a stream that carries NOTHING keeps its current path — retried, not
// reported as reasoning-only — and recovers on the next attempt.
func TestEmptyStreamKeepsItsRetryPathNotTheReasoningError(t *testing.T) {
	p := &scriptedProvider{errs: []error{errEmpty}}
	sess, events, err := retrySession(t, p)
	if err != nil {
		t.Fatalf("an empty first attempt must still be retried and recover: %v", err)
	}
	if countType(events, types.EventRetry) != 1 || p.callCount() != 2 {
		t.Errorf("retries=%d calls=%d, want 1 and 2", countType(events, types.EventRetry), p.callCount())
	}
	for _, e := range events {
		if e.Type == types.EventError {
			t.Errorf("unexpected error event: %+v", e)
		}
	}
	if len(assistantMessages(sess)) != 1 {
		t.Error("exactly the recovered answer should be stored")
	}
}

// ...and when an empty stream never recovers, the error is the EMPTY one, never
// the reasoning one (the two are different failures with different recoveries).
func TestExhaustedEmptyStreamIsStillTheEmptyError(t *testing.T) {
	p := &scriptedProvider{errs: []error{errEmpty, errEmpty, errEmpty}}
	_, _, err := retrySession(t, p)
	if !errors.Is(err, ErrEmptyResponse) || errors.Is(err, ErrReasoningOnly) {
		t.Errorf("exhausted empty stream: %v, want ErrEmptyResponse only", err)
	}
}

// The classifier, directly: what counts as "reasoning only".
func TestReasoningOnlyClassifier(t *testing.T) {
	think := func(s string) types.ContentPart { return types.ContentPart{Thinking: &types.ThinkingPart{Content: s}} }
	msg := func(parts ...types.ContentPart) types.Message {
		return types.Message{Role: types.RoleAssistant, Parts: parts}
	}
	text := func(s string) types.ContentPart { return types.ContentPart{Text: s} }
	tool := types.ContentPart{ToolCall: &types.ToolCall{ID: "t", Name: "X"}}
	for name, tc := range map[string]struct {
		m    types.Message
		want bool
	}{
		"reasoning only":                 {msg(think("hmm")), true},
		"several reasoning blocks":       {msg(think("a"), think("b")), true},
		"redacted reasoning (signature)": {msg(types.ContentPart{Thinking: &types.ThinkingPart{Signature: "sig"}}), true},
		"reasoning + blank text":         {msg(think("hmm"), text("  \n\t")), true},
		"reasoning + text":               {msg(think("hmm"), text("answer")), false},
		"reasoning + tool call":          {msg(think("hmm"), tool), false},
		"text only":                      {msg(text("answer")), false},
		"tool call only":                 {msg(tool), false},
		"nothing at all":                 {msg(), false},
		"empty reasoning block":          {msg(types.ContentPart{Thinking: &types.ThinkingPart{}}), false},
		"text then reasoning":            {msg(text("answer"), think("hmm")), false},
	} {
		if got := reasoningOnly(tc.m); got != tc.want {
			t.Errorf("%s: reasoningOnly = %v, want %v", name, got, tc.want)
		}
	}
}

// splitProvider returns a Response whose ToolCalls are set but whose Message has
// only reasoning — a provider that assembles the two separately. The tool calls
// are real and must run: failing the turn as "reasoning only" while tools are
// pending would execute them AND report an error.
type splitProvider struct {
	fakeThinkingProbeProvider
	mu    sync.Mutex
	calls int
}

func (p *splitProvider) CompleteStream(_ context.Context, _ *types.Request, cb types.StreamCallback) (*types.Response, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.mu.Unlock()
	if n > 0 {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "finished"})
		cb(types.StreamEvent{Type: types.StreamDone})
		return &types.Response{Text: "finished", Message: types.NewAssistantToolCallMessage("finished", "", "", nil)}, nil
	}
	args := json.RawMessage(`{}`)
	cb(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: "thinking about it"})
	cb(types.StreamEvent{Type: types.StreamToolStart, ToolID: "t1", ToolName: "Probe"})
	cb(types.StreamEvent{Type: types.StreamToolEnd, ToolID: "t1", ToolName: "Probe", ToolArgs: args})
	cb(types.StreamEvent{Type: types.StreamDone})
	return &types.Response{
		ToolCalls: []types.ToolCall{{ID: "t1", Name: "Probe", Input: args}},
		Message:   types.NewAssistantToolCallMessage("", "thinking about it", "", nil), // reasoning only, no tool_call part
	}, nil
}

func TestPendingToolCallsAreNeverFailedAsReasoningOnly(t *testing.T) {
	withCleanProviderRegistry(t)
	var thinking string
	p := &splitProvider{}
	p.fakeThinkingProbeProvider = fakeThinkingProbeProvider{name: "ro-split", lastThinking: &thinking}
	providers.AddForTest(p)
	ran := 0
	a := New(AgentOptions{Store: store.NewInMemoryStore(), Tools: []tools.Tool{{
		Def:     types.ToolDef{Name: "Probe", Description: "probe", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Execute: func(context.Context, json.RawMessage) (string, error) { ran++; return "ok", nil },
	}}})
	defer a.Close()
	sess, err := a.NewSession(t.TempDir(), "ro-split/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.PromptAndWait(context.Background(), "go"); err != nil {
		t.Fatalf("a response with tool calls must not fail as reasoning-only: %v", err)
	}
	if ran != 1 {
		t.Errorf("the tool ran %d times, want 1", ran)
	}
}

// The classifier also refuses to call a message "reasoning only" when it carries
// any other kind of content.
func TestReasoningOnlyClassifierOtherContent(t *testing.T) {
	think := types.ContentPart{Thinking: &types.ThinkingPart{Content: "hmm"}}
	for name, extra := range map[string]types.ContentPart{
		"image":       {Image: &types.ImageData{MimeType: "image/png", Base64: "x"}},
		"tool result": {ToolResult: &types.ToolResult{ID: "t", Output: "o"}},
	} {
		m := types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{think, extra}}
		if reasoningOnly(m) {
			t.Errorf("reasoning + %s must not be classified as reasoning-only", name)
		}
	}
}

// The max-iterations progress summary shares runStream, and already refuses a
// reply with no text WITHOUT persisting it. A reasoning-only summary must keep
// that single, unretried failure: the reasoning check belongs to the ReAct loop,
// not inside runStream — there it would turn into the summary's own 3-attempt
// retry loop, re-streaming a degenerate reasoning block twice more.
func TestReasoningOnlyProgressSummaryIsNotRetriedNorStored(t *testing.T) {
	sess, _, err, p := runScripted(t, reply{text: "first turn done"})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.script = []reply{{thinking: "hmm: hmm: hmm:"}}
	p.calls = 0
	p.mu.Unlock()

	before := len(sess.AllMessages())
	sess.mu.Lock() // requestProgressUpdate runs under the turn lock in production
	_, err = sess.requestProgressUpdate(context.Background())
	sess.mu.Unlock()
	if err == nil {
		t.Fatal("a reasoning-only summary must be an error")
	}
	if n := p.callCount(); n != 1 {
		t.Errorf("the summary provider was called %d times, want 1 (no retry of a reasoning-only reply)", n)
	}
	for _, m := range sess.AllMessages()[before:] {
		if m.Role == types.RoleAssistant {
			t.Fatalf("a reasoning-only summary was persisted: %+v", m)
		}
	}
}
