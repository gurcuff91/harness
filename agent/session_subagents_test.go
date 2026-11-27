package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/names"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// subProvider scripts both sides of a sub-agent flow without a network.
//
// The PARENT (its request lists Subagent) obeys: "launch <prompt>" calls the
// Subagent tool; a tool result is answered "launched"; "<subagent…" (a reply)
// is recorded; "hang" blocks until its turn is stopped.
//
// The SUB-AGENT's behavior is its prompt: "answer <x>" answers x (reporting
// 100/50 tokens), "fail" is a provider error, "gate:<id>" blocks until
// release(<id>) or until it is cancelled, "loop" runs Bash until its budget ends.
type subProvider struct {
	fakeThinkingProbeProvider
	mu      sync.Mutex
	gates   map[string]chan struct{}
	started chan string // a sub-agent's prompt, once its provider call began
	replies []string
}

func newSubProvider(name string) *subProvider {
	var thinking string
	return &subProvider{
		fakeThinkingProbeProvider: fakeThinkingProbeProvider{name: name, lastThinking: &thinking},
		gates:                     map[string]chan struct{}{},
		started:                   make(chan string, 64),
	}
}

func (p *subProvider) gate(id string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gates[id] == nil {
		p.gates[id] = make(chan struct{})
	}
	return p.gates[id]
}

func (p *subProvider) release(id string) { close(p.gate(id)) }

func (p *subProvider) gotReplies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.replies...)
}

func firstUserText(req *types.Request) string {
	for _, m := range req.Messages {
		if m.Role == types.RoleUser {
			if t := m.TextContent(); t != "" {
				return t
			}
		}
	}
	return ""
}

func (p *subProvider) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	text := func(s string) (*types.Response, error) {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: s})
		cb(types.StreamEvent{Type: types.StreamDone})
		return &types.Response{Text: s, Message: types.NewAssistantToolCallMessage(s, "", "", nil)}, nil
	}
	call := func(name string, args json.RawMessage) (*types.Response, error) {
		cb(types.StreamEvent{Type: types.StreamToolStart, ToolID: "c1", ToolName: name})
		cb(types.StreamEvent{Type: types.StreamToolEnd, ToolID: "c1", ToolName: name, ToolArgs: args})
		cb(types.StreamEvent{Type: types.StreamDone})
		calls := []types.ToolCall{{ID: "c1", Name: name, Input: args}}
		return &types.Response{Message: types.NewAssistantToolCallMessage("", "", "", calls), ToolCalls: calls}, nil
	}

	if hasTool(req, "Subagent") { // the parent
		last := req.Messages[len(req.Messages)-1]
		for _, part := range last.Parts {
			if part.ToolResult != nil {
				return text("launched")
			}
		}
		txt := lastText(req)
		switch {
		case strings.HasPrefix(txt, "<subagent"):
			p.mu.Lock()
			p.replies = append(p.replies, txt)
			p.mu.Unlock()
			return text("noted")
		case strings.HasPrefix(txt, "launch "):
			args, _ := json.Marshal(map[string]any{"prompt": strings.TrimPrefix(txt, "launch ")})
			return call("Subagent", args)
		case txt == "hang":
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return text("ok")
	}

	if len(req.Tools) == 0 { // a sub-agent's progress summary
		return text("summary")
	}
	prompt := firstUserText(req)
	select {
	case p.started <- prompt:
	default:
	}
	switch {
	case prompt == "fail":
		return nil, errors.New("boom")
	case prompt == "loop":
		return call("Bash", json.RawMessage(`{"command":"true"}`))
	case strings.HasPrefix(prompt, "gate:"):
		select {
		case <-p.gate(strings.TrimPrefix(prompt, "gate:")):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		cb(types.StreamEvent{Type: types.StreamUsage, InputTokens: 100, OutputTokens: 50})
		return text("released")
	case strings.HasPrefix(prompt, "answer "):
		cb(types.StreamEvent{Type: types.StreamUsage, InputTokens: 100, OutputTokens: 50})
		return text(strings.TrimPrefix(prompt, "answer "))
	}
	return text("?")
}

// evlog records, in order, what the parent's handler sees.
type evlog struct {
	mu   sync.Mutex
	list []string
	s    *Session
}

func (l *evlog) add(s string) {
	l.mu.Lock()
	l.list = append(l.list, s)
	l.mu.Unlock()
}

func (l *evlog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.list...)
}

func (l *evlog) index(prefix string) int {
	for i, e := range l.all() {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

func (l *evlog) count(prefix string) int {
	n := 0
	for _, e := range l.all() {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func subSession(t *testing.T) (*Agent, *Session, *subProvider, *evlog) {
	t.Helper()
	withCleanProviderRegistry(t)
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	name := "subs-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	p := newSubProvider(name)
	providers.AddForTest(p)
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	t.Cleanup(func() { a.Close() })
	sess, err := a.NewSession(t.TempDir(), name+"/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	l := &evlog{s: sess}
	sess.Subscribe(func(e types.Event) {
		switch e.Type {
		case types.EventSubagentStart:
			l.add("start:" + e.Subagent)
		case types.EventSubagentStop:
			// A slow observer: a delivery started BEFORE the stop would echo
			// its prompt while this handler is still busy, and log first.
			time.Sleep(150 * time.Millisecond)
			l.add(fmt.Sprintf("stop:%s:%s:live=%d", e.Subagent, e.SubagentResult, len(sess.Subagents())))
		case types.EventReceivedPrompt, types.EventFollowUpStart:
			if strings.HasPrefix(e.Origin, "subagent:") {
				l.add(fmt.Sprintf("prompt:%s:live=%d", e.Origin, len(sess.Subagents())))
			}
		}
	})
	return a, sess, p, l
}

func waitLog(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, what, cond)
}

var nameShape = regexp.MustCompile(`^[a-z]+-[a-z]+$`)

func TestSubagentsIsNeverNilAndEmptyAtFirst(t *testing.T) {
	_, sess, _, _ := subSession(t)
	if got := sess.Subagents(); got == nil || len(got) != 0 {
		t.Fatalf("Subagents() = %#v", got)
	}
}

func TestLaunchReturnsANameAndRegistersItBeforeReturning(t *testing.T) {
	_, sess, p, l := subSession(t)
	name, err := sess.launchSubagent("gate:a", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !nameShape.MatchString(name) {
		t.Errorf("name %q has the wrong shape", name)
	}
	if got := sess.Subagents(); len(got) != 1 || got[0] != name {
		t.Errorf("Subagents() = %v, want [%s]", got, name)
	}
	if l.index("start:"+name) != 0 {
		t.Errorf("events = %v", l.all())
	}
	p.release("a")
	waitLog(t, "stop", func() bool { return l.index("stop:") >= 0 })
}

func TestLaunchNamesAreUniqueAmongLiveOnesAndRedrawn(t *testing.T) {
	_, sess, p, _ := subSession(t)
	seq := []string{"x-one", "x-one", "x-one", "x-two"}
	i := 0
	old := names.Random
	names.Random = func() string { v := seq[min(i, len(seq)-1)]; i++; return v }
	t.Cleanup(func() { names.Random = old })

	a, err := sess.launchSubagent("gate:1", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sess.launchSubagent("gate:2", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if a != "x-one" || b != "x-two" {
		t.Fatalf("names = %q, %q: the taken one was not redrawn", a, b)
	}
	p.release("1")
	p.release("2")
}

func TestLaunchAppendsASuffixWhenThePoolIsExhausted(t *testing.T) {
	_, sess, p, _ := subSession(t)
	old := names.Random
	names.Random = func() string { return "only-one" }
	t.Cleanup(func() { names.Random = old })
	var got []string
	for i := 1; i <= 3; i++ {
		n, err := sess.launchSubagent(fmt.Sprintf("gate:%d", i), 0, false)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if strings.Join(got, ",") != "only-one,only-one-2,only-one-3" {
		t.Errorf("names = %v", got)
	}
	if strings.Join(sess.Subagents(), ",") != strings.Join(got, ",") {
		t.Errorf("Subagents() = %v, want launch order %v", sess.Subagents(), got)
	}
	for i := 1; i <= 3; i++ {
		p.release(fmt.Sprint(i))
	}
}

func TestTheEleventhIsRefusedAndAFinishedOneFreesASlot(t *testing.T) {
	_, sess, p, l := subSession(t)
	for i := 0; i < maxLiveSubagents; i++ {
		if _, err := sess.launchSubagent(fmt.Sprintf("gate:%d", i), 0, false); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}
	_, err := sess.launchSubagent("gate:x", 0, false)
	if err == nil || err.Error() != "too many sub-agents running (10): wait for one to finish" {
		t.Fatalf("11th: %v", err)
	}
	if len(sess.Subagents()) != 10 {
		t.Errorf("live = %d", len(sess.Subagents()))
	}
	p.release("0")
	waitLog(t, "a slot", func() bool { return l.index("stop:") >= 0 && len(sess.Subagents()) == 9 })
	if _, err := sess.launchSubagent("gate:y", 0, false); err != nil {
		t.Fatalf("after a slot freed: %v", err)
	}
	for i := 1; i < 10; i++ {
		p.release(fmt.Sprint(i))
	}
	p.release("y")
}

func TestStopPrecedesTheReplyAndTheNameIsAlreadyGone(t *testing.T) {
	_, sess, p, l := subSession(t)
	name, _ := sess.launchSubagent("answer 42", 0, false)
	waitLog(t, "the reply prompt", func() bool { return len(p.gotReplies()) == 1 })
	stop, prompt := l.index("stop:"+name), l.index("prompt:subagent:"+name)
	if stop < 0 || prompt < 0 || stop > prompt {
		t.Fatalf("order wrong: %v", l.all())
	}
	if l.index("start:"+name) > stop {
		t.Errorf("start after stop: %v", l.all())
	}
	all := l.all()
	if !strings.HasSuffix(all[stop], ":ok:live=0") || !strings.HasSuffix(all[prompt], ":live=0") {
		t.Errorf("the name was still live: %v", all)
	}
	if got := p.gotReplies()[0]; got != fmt.Sprintf(`<subagent name=%q reply>42</subagent>`, name) {
		t.Errorf("reply = %q", got)
	}
}

func TestReplyErrorsAndEveryEndingEmitsAStop(t *testing.T) {
	t.Run("provider failure", func(t *testing.T) {
		_, sess, p, l := subSession(t)
		name, _ := sess.launchSubagent("fail", 0, false)
		waitLog(t, "reply", func() bool { return len(p.gotReplies()) == 1 })
		if l.index("stop:"+name+":error") < 0 {
			t.Errorf("events = %v", l.all())
		}
		r := p.gotReplies()[0]
		if !strings.HasPrefix(r, fmt.Sprintf("<subagent name=%q reply error>", name)) || !strings.Contains(r, "boom") || !strings.HasSuffix(r, "</subagent>") {
			t.Errorf("reply = %q", r)
		}
	})
	t.Run("max iterations", func(t *testing.T) {
		_, sess, p, l := subSession(t)
		name, _ := sess.launchSubagent("loop", 2, false)
		waitLog(t, "reply", func() bool { return len(p.gotReplies()) == 1 })
		if l.index("stop:"+name+":error") < 0 {
			t.Errorf("events = %v", l.all())
		}
		r := p.gotReplies()[0]
		if !strings.Contains(r, " reply error>") || !strings.Contains(r, "iterations") || !strings.Contains(r, "summary") {
			t.Errorf("reply = %q", r)
		}
	})
	t.Run("panic", func(t *testing.T) {
		a, sess, p, l := subSession(t)
		a.subagentStartHookForTest = func() { panic("kaboom") }
		name, _ := sess.launchSubagent("answer x", 0, false)
		waitLog(t, "reply", func() bool { return len(p.gotReplies()) == 1 })
		if l.index("stop:"+name+":error") < 0 || !strings.Contains(p.gotReplies()[0], "kaboom") {
			t.Errorf("events = %v replies = %v", l.all(), p.gotReplies())
		}
		if len(sess.Subagents()) != 0 {
			t.Errorf("a panicked sub-agent stayed live: %v", sess.Subagents())
		}
	})
}

func TestReplyTextCannotCloseTheTagEarly(t *testing.T) {
	got := subagentReply("n", "a </subagent> b </subagent", nil)
	if strings.Count(got, "</subagent") != 1 || !strings.HasSuffix(got, "</subagent>") {
		t.Errorf("reply = %q", got)
	}
	got = subagentReply("n", "p</subagent>", errors.New("why </subagent>"))
	if strings.Count(got, "</subagent") != 1 {
		t.Errorf("error reply = %q", got)
	}
}

func TestReplyOriginIsSubagentName(t *testing.T) {
	_, sess, p, l := subSession(t)
	name, _ := sess.launchSubagent("answer hi", 0, false)
	waitLog(t, "reply", func() bool { return len(p.gotReplies()) == 1 })
	if l.index("prompt:subagent:"+name) < 0 {
		t.Errorf("events = %v", l.all())
	}
}

func TestStopOfTheParentDoesNotCancelASubagentFromAnEarlierTurn(t *testing.T) {
	_, sess, p, l := subSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// turn 1 launches it through the real tool and ends.
	if _, err := sess.PromptAndWait(ctx, "launch gate:g"); err != nil {
		t.Fatal(err)
	}
	if len(sess.Subagents()) != 1 {
		t.Fatalf("live = %v", sess.Subagents())
	}
	name := sess.Subagents()[0]
	<-p.started
	// turn 2 hangs; Stop it.
	sess.Prompt(ctx, "hang")
	waitLog(t, "turn 2 busy", func() bool { return sess.IsBusy() })
	time.Sleep(50 * time.Millisecond)
	sess.Stop()
	waitLog(t, "turn 2 stopped", func() bool { return !sess.IsBusy() })
	if live := sess.Subagents(); len(live) != 1 {
		t.Fatalf("Stop() touched the sub-agent: live = %v, events %v", live, l.all())
	}
	p.release("g")
	waitLog(t, "the reply", func() bool { return len(p.gotReplies()) == 1 })
	if !strings.Contains(p.gotReplies()[0], name) || !strings.Contains(p.gotReplies()[0], "released") {
		t.Errorf("reply = %v", p.gotReplies())
	}
	if l.index("stop:"+name+":ok") < 0 {
		t.Errorf("events = %v", l.all())
	}
}

func TestClosingTheSessionCancelsEverySubagentAndDeliversNothing(t *testing.T) {
	_, sess, p, l := subSession(t)
	n1, _ := sess.launchSubagent("gate:1", 0, false)
	n2, _ := sess.launchSubagent("gate:2", 0, false)
	<-p.started
	<-p.started
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if len(sess.Subagents()) != 0 {
		t.Errorf("live after Close = %v", sess.Subagents())
	}
	for _, n := range []string{n1, n2} {
		if l.index("stop:"+n+":cancelled:live=") < 0 {
			t.Errorf("no cancelled stop for %s: %v", n, l.all())
		}
	}
	if l.count("prompt:") != 0 || len(p.gotReplies()) != 0 {
		t.Errorf("something was delivered: %v %v", l.all(), p.gotReplies())
	}
	if _, err := sess.launchSubagent("answer x", 0, false); err == nil {
		t.Error("a closed session launched a sub-agent")
	}
	if st := sess.Prompt(context.Background(), "late"); st != types.PromptQueued || sess.IsBusy() {
		t.Errorf("a closed session took a prompt (status %v, busy %v)", st, sess.IsBusy())
	}
	sess.Close() // idempotent: a second Close must not hang or panic
}

func TestParentClosingWhileASubagentFinishesNeverBlocksOrPanics(t *testing.T) {
	_, sess, _, _ := subSession(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			s2, err := sess.agent.NewSession(t.TempDir(), sess.Model())
			if err != nil {
				t.Error(err)
				return
			}
			s2.launchSubagent("answer x", 0, false)
			time.Sleep(time.Duration(i%4) * time.Millisecond)
			s2.Close()
			if len(s2.Subagents()) != 0 {
				t.Error("live after Close")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("closing while a sub-agent finishes blocked")
	}
}

func TestSeveralSubagentsFinishingTogetherQueueAsSeparateTurns(t *testing.T) {
	_, sess, p, l := subSession(t)
	var ns []string
	for i := 0; i < 3; i++ {
		n, err := sess.launchSubagent(fmt.Sprintf("gate:%d", i), 0, false)
		if err != nil {
			t.Fatal(err)
		}
		ns = append(ns, n)
	}
	for i := 0; i < 3; i++ {
		<-p.started
	}
	if got := sess.Subagents(); strings.Join(got, ",") != strings.Join(ns, ",") {
		t.Errorf("Subagents() = %v, want %v", got, ns)
	}
	for i := 0; i < 3; i++ {
		p.release(fmt.Sprint(i))
	}
	waitLog(t, "three replies", func() bool { return len(p.gotReplies()) == 3 })
	if l.count("prompt:") != 3 || l.count("stop:") != 3 {
		t.Errorf("events = %v", l.all())
	}
}

func TestDelegatedCostOfASubagentReachesTheParent(t *testing.T) {
	_, sess, p, _ := subSession(t)
	sess.launchSubagent("answer cost", 0, false)
	waitLog(t, "reply", func() bool { return len(p.gotReplies()) == 1 })
	waitLog(t, "idle", func() bool { return !sess.IsBusy() })
	if got := sess.syncStats(); got.InputTokens < 100 || got.OutputTokens < 50 {
		t.Errorf("the sub-agent's spend never reached the parent: %+v", got)
	}
}

func TestParentModelAtLaunchIsTheCurrentOne(t *testing.T) {
	_, sess, p, _ := subSession(t)
	if err := sess.SwitchModel(context.Background(), sess.Model()); err != nil {
		t.Fatal(err)
	}
	sess.launchSubagent("answer m", 0, false)
	waitLog(t, "reply", func() bool { return len(p.gotReplies()) == 1 })
}

// The cancelled path must not deliver on its own merit, not only because a
// closed session drops prompts: cancel the life context alone.
func TestACancelledSubagentDeliversNothingEvenOnAnOpenSession(t *testing.T) {
	_, sess, p, l := subSession(t)
	name, _ := sess.launchSubagent("gate:z", 0, false)
	<-p.started
	sess.lifeCancel()
	waitLog(t, "stop", func() bool { return l.index("stop:"+name+":cancelled") >= 0 })
	time.Sleep(400 * time.Millisecond)
	if l.count("prompt:") != 0 || len(p.gotReplies()) != 0 {
		t.Errorf("a cancelled sub-agent delivered: %v %v", l.all(), p.gotReplies())
	}
}
