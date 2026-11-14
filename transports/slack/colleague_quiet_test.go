package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
)

// posted collects every chat.postMessage the transport sends.
type posted struct {
	mu    sync.Mutex
	texts []string
}

func (p *posted) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...)
}

// drainTransport builds a Transport whose bot posts to a local fake Slack, and
// runs drain over the given events, returning what reached the channel.
func drainTransport(t *testing.T, events []client.Event) []string {
	t.Helper()
	got := &posted{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/chat.postMessage") {
			r.ParseForm()
			got.mu.Lock()
			got.texts = append(got.texts, r.Form.Get("text"))
			got.mu.Unlock()
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	if _, err := url.Parse(ts.URL); err != nil {
		t.Fatal(err)
	}
	tr := &Transport{
		bot:    NewBot(ts.URL, "xoxc-test", "xoxd-test"),
		logger: logx.NewNilLogger(),
		pumps:  map[string]*channelPump{},
	}
	p := &channelPump{channelID: "C123", sessionID: "s1"}
	tr.pumps["C123"] = p

	ch := make(chan client.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	done := make(chan struct{})
	go func() { tr.drain(context.Background(), p, ch); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not return")
	}
	if p.quiet {
		t.Error("quiet must be cleared at turn_end")
	}
	return got.all()
}

func turn(origin, answer string) []client.Event {
	return []client.Event{
		{Type: "received_prompt", Origin: origin, Text: "the question"},
		{Type: "turn_start"},
		{Type: "text", Delta: answer},
		{Type: "text_end"},
		{Type: "turn_end"},
	}
}

// A colleague's question is answered to the colleague: nothing reaches the channel.
func TestColleaguePromptAnswerIsNotPostedToSlack(t *testing.T) {
	if got := drainTransport(t, turn("colleague", "the answer for the colleague")); len(got) != 0 {
		t.Errorf("posted %q: a colleague's answer must stay out of the channel", got)
	}
}

// Everything else keeps posting: a scheduled task (work the channel asked for), a
// typed message, and a prompt with no origin.
func TestOtherOriginsStillPostTheirAnswer(t *testing.T) {
	for _, origin := range []string{"scheduled", "slack", "user", ""} {
		got := drainTransport(t, turn(origin, "the answer"))
		if len(got) != 1 || !strings.Contains(got[0], "the answer") {
			t.Errorf("origin %q: posted %q, want the answer once", origin, got)
		}
	}
}

// A colleague turn is quiet for errors too (nothing about it belongs in the
// channel) and for a tool-call flush mid-turn.
func TestColleagueTurnIsQuietForErrorsAndToolFlushes(t *testing.T) {
	got := drainTransport(t, []client.Event{
		{Type: "received_prompt", Origin: "colleague"},
		{Type: "turn_start"},
		{Type: "text", Delta: "let me check"},
		{Type: "tool_call", ToolName: "Bash"},
		{Type: "error", Message: "boom"},
		{Type: "turn_end"},
	})
	if len(got) != 0 {
		t.Errorf("posted %q, want nothing for a colleague's turn", got)
	}
}

// The quiet flag belongs to ONE turn: the next typed message after a colleague's
// is answered normally; and a colleague prompt that starts later (a queued
// follow-up) silences only its own turn.
func TestQuietDoesNotLeakAcrossTurns(t *testing.T) {
	ev := append(turn("colleague", "for the colleague"), turn("slack", "for the user")...)
	ev = append(ev, []client.Event{
		{Type: "follow_up_start", Origin: "colleague"},
		{Type: "turn_start"}, {Type: "text", Delta: "again for the colleague"}, {Type: "text_end"}, {Type: "turn_end"},
	}...)
	ev = append(ev, turn("slack", "for the user, second")...)
	got := drainTransport(t, ev)
	if len(got) != 2 || !strings.Contains(got[0], "for the user") || !strings.Contains(got[1], "second") {
		t.Errorf("posted %q, want exactly the two answers to the user", got)
	}
}

// A turn stopped before turn_end (user interrupt) must not leave the channel
// muted for the next prompt: the next echo resets it regardless.
func TestQuietIsResetByTheNextPromptEcho(t *testing.T) {
	ev := []client.Event{
		{Type: "received_prompt", Origin: "colleague"},
		{Type: "turn_start"},
		{Type: "stop"}, // no turn_end seen
	}
	ev = append(ev, turn("slack", "for the user")...)
	got := drainTransport(t, ev)
	if len(got) != 1 || !strings.Contains(got[0], "for the user") {
		t.Errorf("posted %q, want the user's answer", got)
	}
}

// The directive tells the agent how to behave without context tags.
func TestDirectiveCoversMessagesWithoutContextTags(t *testing.T) {
	for _, want := range []string{
		"### Messages without context tags",
		`(see "Messages without context tags" below)`,
		"NO <slack:user> tag",
		"Do NOT @mention anyone",
		"is NOT posted in this channel",
		"scheduled task, your reply IS posted",
	} {
		if !strings.Contains(Directive, want) {
			t.Errorf("Directive lacks %q", want)
		}
	}
}

// A scheduled prompt still shows the typing indicator the moment it arrives
// (before turn_start); a colleague's does not — nobody in the channel is waiting
// on it. The stream is kept open so drain's final stopTyping doesn't hide it.
func TestTypingIndicatorOnlyForScheduledEcho(t *testing.T) {
	for origin, want := range map[string]bool{"scheduled": true, "colleague": false} {
		tr := &Transport{logger: logx.NewNilLogger(), pumps: map[string]*channelPump{}}
		p := &channelPump{channelID: "C1", sessionID: "s"}
		ch := make(chan client.Event, 1)
		ch <- client.Event{Type: "received_prompt", Origin: origin}
		done := make(chan struct{})
		go func() { tr.drain(context.Background(), p, ch); close(done) }()

		deadline := time.Now().Add(2 * time.Second)
		got := false
		for time.Now().Before(deadline) {
			p.mu.Lock()
			got = p.typingCancel != nil
			p.mu.Unlock()
			if got == want && want {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got != want {
			t.Errorf("origin %q: typing=%v, want %v", origin, got, want)
		}
		close(ch)
		<-done
	}
}
