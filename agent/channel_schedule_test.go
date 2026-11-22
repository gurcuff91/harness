package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/schedule"
	"github.com/gurcuff91/harness/types"
)

// A turn whose origin carries a sub-conversation of a bound conversation
// ("fake:ONE:chat-7" under "fake:ONE") is routed to that conversation; an
// origin that merely shares a prefix ("fake:ONEX") is not.
func TestTapRoutesSubConversationsByPrefix(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	sess, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "one"}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	for origin, want := range map[string]bool{
		"fake:ONE":        true,
		"fake:ONE:chat-7": true,
		"fake:ONEX":       false,
		"fake:ON":         false,
		"user":            false,
		"":                false,
	} {
		if got := sess.channels.listensTo(origin); got != want {
			t.Errorf("listensTo(%q) = %v, want %v", origin, got, want)
		}
	}
	before := len(ch.eventsOf("ONE"))
	if _, err := sess.PromptAndWait(context.Background(), "hi", PromptWithOrigin("fake:ONE:chat-7")); err != nil {
		t.Fatal(err)
	}
	evs := ch.eventsOf("ONE")[before:]
	if len(evs) == 0 || evs[0].Origin != "fake:ONE:chat-7" || evs[len(evs)-1].Type != types.EventTurnEnd {
		t.Fatalf("the sub-conversation's turn did not reach the channel: %+v", evs)
	}
	before = len(ch.eventsOf("ONE"))
	if _, err := sess.PromptAndWait(context.Background(), "hi", PromptWithOrigin("fake:ONEX")); err != nil {
		t.Fatal(err)
	}
	if n := len(ch.eventsOf("ONE")) - before; n != 0 {
		t.Errorf("an unrelated origin reached the channel (%d events)", n)
	}
}

// scheduleOrigin records the turn's origin only when it is a channel
// conversation the session listens to; never a colleague's, the person's, or
// one outside a turn.
func TestScheduleOriginIsTheTurnsChannelConversation(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	sess, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "one"}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// A conversation that delivers with its sender as origin (as colleagues
	// do) is listened to, but a colleague's turn never records an origin.
	sess.channels.mu.Lock()
	sess.channels.byPrompt["colleague:x"] = sess.channels.byOrigin["fake:ONE"]
	sess.channels.mu.Unlock()
	if !sess.channels.listensTo("colleague:x") {
		t.Fatal("setup: the colleague origin must be routed")
	}
	for origin, want := range map[string]string{
		"fake:ONE":        "fake:ONE",
		"fake:ONE:chat-7": "fake:ONE:chat-7",
		"fake:OTHER":      "",
		"user":            "",
		"colleague:x":     "",
		"scheduled":       "",
	} {
		sess.emit(types.Event{Type: types.EventReceivedPrompt, Origin: origin})
		if got := sess.scheduleOrigin(); got != want {
			t.Errorf("turn of %q: scheduleOrigin = %q, want %q", origin, got, want)
		}
		sess.emit(types.Event{Type: types.EventTurnEnd})
		if got := sess.scheduleOrigin(); got != "" {
			t.Errorf("after turn_end: %q", got)
		}
	}
}

// A schedule created from a channel conversation fires back INTO it (the
// channel gets the turn), wrapped as scheduled; one without an origin, or
// whose conversation the session no longer listens to, fires as "scheduled".
func TestAFiredScheduleGoesBackToItsChannel(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	sess, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "one"}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var prompts []types.Event
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventReceivedPrompt || e.Type == types.EventFollowUpStart {
			<-mu
			prompts = append(prompts, e)
			mu <- struct{}{}
		}
	})
	snapshot := func() []types.Event {
		<-mu
		defer func() { mu <- struct{}{} }()
		return append([]types.Event(nil), prompts...)
	}

	before := len(ch.eventsOf("ONE"))
	a.fireScheduledPrompt(schedule.Schedule{Slug: "standup", Prompt: "post the standup", Owner: sess.ID(), Origin: "fake:ONE:chat-7"})
	waitFor(t, "the turn reaches the channel", func() bool {
		evs := ch.eventsOf("ONE")[before:]
		return len(evs) > 0 && evs[len(evs)-1].Type == types.EventTurnEnd
	})
	p := snapshot()
	if len(p) != 1 || p[0].Origin != "fake:ONE:chat-7" || !strings.Contains(p[0].Output, `<scheduled slug="standup">`) || !strings.Contains(p[0].Output, "post the standup") {
		t.Fatalf("fired prompt = %+v", p)
	}

	// No origin → scheduled, the channel gets nothing.
	before = len(ch.eventsOf("ONE"))
	a.fireScheduledPrompt(schedule.Schedule{Slug: "plain", Prompt: "x", Owner: sess.ID()})
	waitFor(t, "second fire", func() bool { return len(snapshot()) == 2 })
	// A conversation the session does not listen to → scheduled too.
	a.fireScheduledPrompt(schedule.Schedule{Slug: "gone", Prompt: "y", Owner: sess.ID(), Origin: "fake:OTHER"})
	waitFor(t, "third fire", func() bool { return len(snapshot()) == 3 })
	p = snapshot()
	for _, e := range p[1:] {
		if e.Origin != OriginScheduled || !strings.HasPrefix(e.Output, "<scheduled slug=") {
			t.Errorf("fallback prompt = %+v", e)
		}
	}
	waitFor(t, "turns settle", func() bool { return !sess.IsBusy() })
	if n := len(ch.eventsOf("ONE")) - before; n != 0 {
		t.Errorf("a schedule without (a listened) origin reached the channel: %d events", n)
	}
}

// The Schedule tool stores the origin the session gives it.
func TestTheScheduleToolStoresTheTurnsOrigin(t *testing.T) {
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	a.opts.EnableScheduler = true
	st, err := schedule.Open()
	if err != nil {
		t.Fatal(err)
	}
	a.schedStore = st
	sess, err := a.NewSession(t.TempDir(), model, WithSessionChannels(types.InputChannel{Type: "fake", Ref: "one"}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.emit(types.Event{Type: types.EventReceivedPrompt, Origin: "fake:ONE:chat-7"})
	if _, _, err := sess.tools.Run(context.Background(), "Schedule", json.RawMessage(`{"slug":"s1","cron":"@daily","prompt":"p"}`)); err != nil {
		t.Fatal(err)
	}
	sess.emit(types.Event{Type: types.EventTurnEnd})
	for _, sc := range a.schedStore.List() {
		if sc.Slug == "s1" {
			if sc.Origin != "fake:ONE:chat-7" || sc.Owner != sess.ID() {
				t.Fatalf("stored %+v", sc)
			}
			return
		}
	}
	t.Fatal("schedule not stored")
}
