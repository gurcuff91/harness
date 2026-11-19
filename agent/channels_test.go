package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// fakeChannel records what an agent asks of a Channel.
type fakeChannel struct {
	typ        string
	resolveErr error
	listenErr  error
	toolName   string

	mu        sync.Mutex
	listening map[string]func(Inbound)
	stopped   []string
	events    map[string][]types.Event
}

func newFakeChannel(typ string) *fakeChannel {
	return &fakeChannel{typ: typ, toolName: "FakeChanTool", listening: map[string]func(Inbound){}, events: map[string][]types.Event{}}
}

func (f *fakeChannel) Type() string { return f.typ }
func (f *fakeChannel) Resolve(_ context.Context, ref string) (string, error) {
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return strings.ToUpper(strings.TrimPrefix(ref, "#")), nil // "#general" → "GENERAL"
}
func (f *fakeChannel) Listen(_ context.Context, id string, deliver func(Inbound)) (func(), error) {
	if f.listenErr != nil {
		return nil, f.listenErr
	}
	f.mu.Lock()
	f.listening[id] = deliver
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.listening, id)
		f.stopped = append(f.stopped, id)
		f.mu.Unlock()
	}, nil
}
func (f *fakeChannel) HandleEvent(id string, e types.Event) {
	f.mu.Lock()
	f.events[id] = append(f.events[id], e)
	f.mu.Unlock()
}
func (f *fakeChannel) Name(_ context.Context, id string) string { return "#" + strings.ToLower(id) }
func (f *fakeChannel) Directive(ids []string) string {
	return "## FAKE CHANNEL " + strings.Join(ids, ",")
}
func (f *fakeChannel) Tools() []tools.Tool {
	return []tools.Tool{{
		Def:     types.ToolDef{Name: f.toolName, Description: "fake", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Execute: func(context.Context, json.RawMessage) (string, error) { return "ok", nil },
	}}
}
func (f *fakeChannel) deliver(id, text string) {
	f.mu.Lock()
	d := f.listening[id]
	f.mu.Unlock()
	d(Inbound{Text: text})
}
func (f *fakeChannel) eventsOf(id string) []types.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.Event(nil), f.events[id]...)
}
func (f *fakeChannel) isListening(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.listening[id]
	return ok
}

// channelAgent is an agent with the fake channel and a fake model that answers
// every prompt with "answer to <prompt>", on isolated stores.
func channelAgent(t *testing.T, ch Channel) (*Agent, string, *store.InMemoryStore) {
	t.Helper()
	withCleanProviderRegistry(t)
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	name := "chan-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	var thinking string
	providers.AddForTest(&echoProvider{fakeThinkingProbeProvider{name: name, lastThinking: &thinking}})
	st := store.NewInMemoryStore()
	var chans []Channel
	if ch != nil {
		chans = []Channel{ch}
	}
	a := New(AgentOptions{Store: st, Channels: chans})
	t.Cleanup(func() { a.Close() })
	return a, name + "/fake-model", st
}

// echoProvider answers "answer to <last user text>".
type echoProvider struct{ fakeThinkingProbeProvider }

func (p *echoProvider) CompleteStream(_ context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	last := ""
	for _, m := range req.Messages {
		if m.Role == types.RoleUser && len(m.Parts) > 0 {
			last = m.Parts[0].Text
		}
	}
	out := "answer to " + last
	cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: out})
	cb(types.StreamEvent{Type: types.StreamDone})
	return &types.Response{Text: out, Message: types.NewAssistantToolCallMessage(out, "", "", nil)}, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func textOf(evs []types.Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == types.EventStreamTextDelta {
			b.WriteString(e.Delta)
		}
	}
	return b.String()
}

func hasEvent(evs []types.Event, typ types.EventType) bool {
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// A bound session listens, gets the channel's directive and tools, and only the
// turns its channel started reach HandleEvent — on the right conversation.
func TestChannelTurnsGoToTheirConversationOnly(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	sess, err := a.NewSession(t.TempDir(), model, WithSessionChannels(
		types.InputChannel{Type: "fake", Ref: "#one"}, types.InputChannel{Type: "fake", Ref: "two"}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if !ch.isListening("ONE") || !ch.isListening("TWO") {
		t.Fatal("not listening to both conversations")
	}
	if got := sess.Channels(); len(got) != 2 || got[0] != (ChannelInfo{Type: "fake", ID: "ONE", Name: "#one"}) || got[1].ID != "TWO" || got[1].Name != "#two" {
		t.Errorf("Channels() = %v", got)
	}
	if !strings.Contains(sess.systemPrompt, "## FAKE CHANNEL ONE,TWO") {
		t.Error("the channel's directive (naming both ids) is not in the system prompt")
	}
	found := false
	for _, d := range sess.Tools() {
		found = found || d.Name == "FakeChanTool"
	}
	if !found {
		t.Error("the channel's tools are not in the session")
	}

	// A prompt from the terminal: nothing reaches the channel.
	if _, err := sess.PromptAndWait(context.Background(), "from the user"); err != nil {
		t.Fatal(err)
	}
	// A message in conversation TWO: its turn reaches TWO only, with the origin.
	ch.deliver("TWO", "hi from two")
	waitFor(t, "the channel turn to end", func() bool { return hasEvent(ch.eventsOf("TWO"), types.EventTurnEnd) })

	if evs := ch.eventsOf("ONE"); len(evs) != 0 {
		t.Errorf("conversation ONE received %d events", len(evs))
	}
	evs := ch.eventsOf("TWO")
	if got := textOf(evs); got != "answer to hi from two" {
		t.Errorf("TWO got text %q", got)
	}
	if evs[0].Type != types.EventReceivedPrompt || evs[0].Origin != "fake:TWO" {
		t.Errorf("first event = %v origin %q, want the prompt echo with origin fake:TWO", evs[0].Type, evs[0].Origin)
	}
	if strings.Contains(textOf(evs), "from the user") {
		t.Error("the terminal turn leaked into the channel")
	}
	// Events outside any turn after a channel turn (a manual compaction) do not
	// belong to it.
	n := len(ch.eventsOf("TWO"))
	_ = sess.Compact(context.Background())
	if len(ch.eventsOf("TWO")) != n {
		t.Error("an event outside the channel's turn (compaction) reached the channel")
	}
	// The user prompt after a channel turn is not published either.
	if _, err := sess.PromptAndWait(context.Background(), "user again"); err != nil {
		t.Fatal(err)
	}
	if len(ch.eventsOf("TWO")) != n {
		t.Error("a later user turn reached the channel")
	}
	// The origin is persisted with the message, like any other origin.
	var origin string
	for _, m := range sess.AllMessages() {
		if m.Role == types.RoleUser && m.Meta != nil && strings.HasPrefix(m.Meta.Origin, "fake:") {
			origin = m.Meta.Origin
		}
	}
	if origin != "fake:TWO" {
		t.Errorf("persisted origin = %q", origin)
	}
}

// One conversation, one session: across sessions (and the registry is what
// another process would see too). Closing releases it.
func TestAConversationHasOneListener(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	s1, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "#C1"}))
	if !errors.Is(err, ErrChannelTaken) || !strings.Contains(err.Error(), s1.ID()) {
		t.Fatalf("second listener: err=%v, want ErrChannelTaken naming %s", err, s1.ID())
	}
	if n := len(config.GetSettingsManager().ChannelClaims()); n != 1 {
		t.Fatalf("%d claims after a refused open, want 1", n)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if ch.isListening("C1") || len(config.GetSettingsManager().ChannelClaims()) != 0 {
		t.Fatal("closing the session did not stop listening and release the claim")
	}
	s2, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	s2.Close()
}

// A claim held by another LIVE process blocks; one left by a dead process is taken.
func TestAClaimFromAnotherProcess(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	sm := config.GetSettingsManager()
	alive := func(config.ChannelClaim) bool { return true }
	// The test's parent (go test) is a live process of this user on every OS;
	// pid 1 does not exist on Windows.
	other := os.Getppid()
	_, _ = sm.ClaimChannel(config.ChannelClaim{Type: "fake", ID: "C1", Session: "elsewhere", PID: other, CWD: "/other"}, alive)
	_, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if !errors.Is(err, ErrChannelTaken) || !strings.Contains(err.Error(), "/other") {
		t.Fatalf("live foreign claim: err=%v", err)
	}
	_ = sm.ReleaseChannel("fake", "C1", "elsewhere", other)
	_, _ = sm.ClaimChannel(config.ChannelClaim{Type: "fake", ID: "C1", Session: "dead", PID: 999999999}, alive)
	s, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if err != nil {
		t.Fatalf("dead foreign claim must be taken over: %v", err)
	}
	s.Close()
}

// Every failure leaves nothing behind: no session, no claim, no listener.
func TestAFailedOpenLeavesNothing(t *testing.T) {
	cases := map[string]struct {
		mut  func(*fakeChannel)
		req  []types.InputChannel
		want error
	}{
		"unknown type":                      {nil, []types.InputChannel{{Type: "nope", Ref: "x"}}, ErrChannelUnavailable},
		"empty ref":                         {nil, []types.InputChannel{{Type: "fake", Ref: " "}}, ErrChannelInvalid},
		"resolve fails":                     {func(f *fakeChannel) { f.resolveErr = errors.New("no such channel") }, []types.InputChannel{{Type: "fake", Ref: "x"}}, ErrChannelInvalid},
		"listen fails":                      {func(f *fakeChannel) { f.listenErr = errors.New("socket down") }, []types.InputChannel{{Type: "fake", Ref: "a"}, {Type: "fake", Ref: "b"}}, nil},
		"tool without the channel's prefix": {func(f *fakeChannel) { f.toolName = "PostMessage" }, []types.InputChannel{{Type: "fake", Ref: "a"}}, ErrChannelInvalid},
		"second is taken":                   {nil, []types.InputChannel{{Type: "fake", Ref: "a"}, {Type: "fake", Ref: "held"}}, ErrChannelTaken},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ch := newFakeChannel("fake")
			if tc.mut != nil {
				tc.mut(ch)
			}
			a, model, st := channelAgent(t, ch)
			_, _ = config.GetSettingsManager().ClaimChannel(config.ChannelClaim{Type: "fake", ID: "HELD", Session: "x", PID: os.Getppid()}, func(config.ChannelClaim) bool { return true })
			_, err := a.NewSession(t.TempDir(), model, WithSessionChannels(tc.req...))
			if err == nil {
				t.Fatal("open succeeded")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			for _, c := range config.GetSettingsManager().ChannelClaims() {
				if c.ID != "HELD" {
					t.Errorf("claim left behind: %+v", c)
				}
			}
			if ch.isListening("A") || ch.isListening("B") {
				t.Error("listener left behind")
			}
			if metas, _ := st.ListMetas(""); len(metas) != 0 {
				t.Errorf("%d sessions left in the store", len(metas))
			}
		})
	}
}

// Channels are explicit and never persisted: a resume without them listens to
// nothing; a resume with them binds; asking an already-open session for a
// channel it does not have fails instead of being ignored.
func TestResumeBindsOnlyWhatItIsGiven(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	s, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	// Already open: the same channel again is fine (no-op), a new one is refused.
	if _, err := a.ResumeSession(id, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "#c1"})); err != nil {
		t.Errorf("same channel on an open session: %v", err)
	}
	if _, err := a.ResumeSession(id, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c2"})); !errors.Is(err, ErrChannelInvalid) {
		t.Errorf("new channel on an open session: err=%v", err)
	}
	s.Close()

	r, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Channels()) != 0 || ch.isListening("C1") {
		t.Fatal("a resume without channels listens")
	}
	r.Close()
	r, err = a.ResumeSession(id, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c2"}))
	if err != nil {
		t.Fatal(err)
	}
	if !ch.isListening("C2") || !strings.Contains(r.systemPrompt, "## FAKE CHANNEL C2") {
		t.Fatal("resume with a channel does not listen or lacks the directive")
	}
	r.Close()
	if ch.isListening("C2") {
		t.Fatal("still listening after close")
	}
}

// Closing the agent stops every listener and releases every claim.
func TestAgentCloseReleasesEverything(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	for i := 0; i < 3; i++ {
		if _, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: fmt.Sprint("c", i)})); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	if n := len(config.GetSettingsManager().ChannelClaims()); n != 0 {
		t.Fatalf("%d claims after Agent.Close", n)
	}
	for i := 0; i < 3; i++ {
		if ch.isListening(fmt.Sprint("C", i)) {
			t.Fatal("listener alive after Agent.Close")
		}
	}
}

// A session with no channels never touches the registry or the tap.
func TestNoChannelsNoCost(t *testing.T) {
	a, model, _ := channelAgent(t, nil)
	s, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.channels != nil || len(s.Channels()) != 0 {
		t.Fatal("a session without channels has channel state")
	}
}

// The turn owner is decided by each prompt echo: an echo of an unbound origin
// clears it, and turn_end always ends it.
func TestTapFollowsEachTurnsOrigin(t *testing.T) {
	ch := newFakeChannel("fake")
	b := &boundChannel{ch: ch, id: "C1"}
	sc := &sessionChannels{byOrigin: map[string]*boundChannel{"fake:C1": b}}
	sc.tap(types.Event{Type: types.EventReceivedPrompt, Origin: "fake:C1"})
	sc.tap(types.Event{Type: types.EventStreamTextDelta, Delta: "a"})
	// No turn_end (a turn cut short) and the next prompt is the user's: nothing more.
	sc.tap(types.Event{Type: types.EventFollowUpStart, Origin: "user"})
	sc.tap(types.Event{Type: types.EventStreamTextDelta, Delta: "b"})
	sc.tap(types.Event{Type: types.EventTurnEnd})
	// After turn_end, an event with no echo before it is nobody's.
	sc.tap(types.Event{Type: types.EventReceivedPrompt, Origin: "fake:C1"})
	sc.tap(types.Event{Type: types.EventTurnEnd})
	sc.tap(types.Event{Type: types.EventCompactStart})
	if got := textOf(ch.eventsOf("C1")); got != "a" {
		t.Errorf("channel text = %q, want only the channel turn's", got)
	}
	evs := ch.eventsOf("C1")
	if last := evs[len(evs)-1]; last.Type != types.EventTurnEnd {
		t.Errorf("last event = %v, want turn_end (the compaction after it must not reach the channel)", last.Type)
	}
}

// Reconfiguring a live session (a server profile bound to it changed) keeps
// the channel's tools and directive.
func TestReconfigureKeepsTheChannel(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	s, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := a.ReconfigureSession(s.ID(), WithSessionDirectives("## OTHER")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromptAndWait(context.Background(), "x"); err != nil { // applies at the turn boundary
		t.Fatal(err)
	}
	if !strings.Contains(s.systemPrompt, "## FAKE CHANNEL C1") || !strings.Contains(s.systemPrompt, "## OTHER") {
		t.Error("reconfigure dropped the channel directive (or the new one)")
	}
	found := false
	for _, d := range s.Tools() {
		found = found || d.Name == "FakeChanTool"
	}
	if !found {
		t.Error("reconfigure dropped the channel's tools")
	}
}

// A channel tool whose name another source already has (here an agent tool
// with the channel's own prefix) fails the open instead of shadowing it.
func TestAChannelToolNameClashFailsTheOpen(t *testing.T) {
	ch := newFakeChannel("fake")
	withCleanProviderRegistry(t)
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	name := "clash-" + strings.NewReplacer("/", "-").Replace(t.Name())
	var thinking string
	providers.AddForTest(&echoProvider{fakeThinkingProbeProvider{name: name, lastThinking: &thinking}})
	a := New(AgentOptions{Store: store.NewInMemoryStore(), Channels: []Channel{ch}, Tools: ch.Tools()})
	defer a.Close()
	_, err := a.NewSession(t.TempDir(), name+"/fake-model", WithSessionChannels(types.InputChannel{Type: "fake", Ref: "a"}))
	if !errors.Is(err, ErrChannelInvalid) || !strings.Contains(err.Error(), "already in the session") {
		t.Fatalf("err = %v", err)
	}
	if len(config.GetSettingsManager().ChannelClaims()) != 0 {
		t.Error("a claim was left behind")
	}
}

func TestChannelToolPrefix(t *testing.T) {
	for typ, want := range map[string]string{"slack": "Slack", "telegram": "Telegram", "x": "X", "": ""} {
		if got := channelToolPrefix(typ); got != want {
			t.Errorf("channelToolPrefix(%q) = %q, want %q", typ, got, want)
		}
	}
}
