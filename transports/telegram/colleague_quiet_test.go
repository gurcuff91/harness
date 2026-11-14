package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	agentstore "github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

// fakeTelegram is a Bot API that records every sendMessage text and every
// sendChatAction ("typing") it receives.
type fakeTelegram struct {
	*httptest.Server
	mu      sync.Mutex
	texts   []string
	typings int
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Text string `json:"text"`
		}
		json.Unmarshal(raw, &body)
		f.mu.Lock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			f.texts = append(f.texts, body.Text)
		case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
			f.typings++
		}
		f.mu.Unlock()
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTelegram) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func (f *fakeTelegram) typingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typings
}

func (f *fakeTelegram) bot() *Bot {
	b := NewBot("TOKEN")
	b.api = f.URL + "/botTOKEN"
	return b
}

// drainEvents runs drain over the events against a fake Telegram and returns what
// reached the chat.
func drainEvents(t *testing.T, events []client.Event) []string {
	t.Helper()
	fake := newFakeTelegram(t)
	tr := &Transport{bot: fake.bot(), logger: logx.NewNilLogger(), pumps: map[int64]*chatPump{}}
	p := &chatPump{chatID: 42, sessionID: "s1"}
	tr.pumps[42] = p
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
	return fake.sent()
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

// A colleague's question is answered to the colleague: nothing reaches the chat.
func TestColleaguePromptAnswerIsNotSentToTelegram(t *testing.T) {
	if got := drainEvents(t, turn("colleague", "the answer for the colleague")); len(got) != 0 {
		t.Errorf("sent %q: a colleague's answer must stay out of the chat", got)
	}
}

// Everything else keeps sending: a scheduled task (work the chat asked for), a
// typed message, and a prompt with no origin.
func TestOtherOriginsStillSendTheirAnswer(t *testing.T) {
	for _, origin := range []string{"scheduled", "telegram", "user", ""} {
		got := drainEvents(t, turn(origin, "the answer"))
		if len(got) != 1 || !strings.Contains(got[0], "the answer") {
			t.Errorf("origin %q: sent %q, want the answer once", origin, got)
		}
	}
}

// A colleague turn is quiet for errors too, and for text flushed around a tool
// call.
func TestColleagueTurnIsQuietForErrorsAndToolText(t *testing.T) {
	got := drainEvents(t, []client.Event{
		{Type: "received_prompt", Origin: "colleague"},
		{Type: "turn_start"},
		{Type: "text", Delta: "let me check"},
		{Type: "text_end"},
		{Type: "tool_call", ToolName: "Bash"},
		{Type: "error", Message: "boom"},
		{Type: "turn_end"},
	})
	if len(got) != 0 {
		t.Errorf("sent %q, want nothing for a colleague's turn", got)
	}
}

// Quiet belongs to ONE turn: a typed message after a colleague's is answered
// normally, and a queued colleague follow-up silences only its own turn.
func TestQuietDoesNotLeakAcrossTurns(t *testing.T) {
	ev := append(turn("colleague", "for the colleague"), turn("telegram", "for the user")...)
	ev = append(ev, []client.Event{
		{Type: "follow_up_start", Origin: "colleague"},
		{Type: "turn_start"}, {Type: "text", Delta: "again for the colleague"}, {Type: "text_end"}, {Type: "turn_end"},
	}...)
	ev = append(ev, turn("telegram", "for the user, second")...)
	got := drainEvents(t, ev)
	if len(got) != 2 || !strings.Contains(got[0], "for the user") || !strings.Contains(got[1], "second") {
		t.Errorf("sent %q, want exactly the two answers to the user", got)
	}
}

// A turn that ends without turn_end (an interrupt) must not leave the chat muted:
// the next prompt echo resets it.
func TestQuietIsResetByTheNextPromptEcho(t *testing.T) {
	ev := []client.Event{
		{Type: "received_prompt", Origin: "colleague"},
		{Type: "turn_start"},
		{Type: "stop"},
	}
	ev = append(ev, turn("telegram", "for the user")...)
	got := drainEvents(t, ev)
	if len(got) != 1 || !strings.Contains(got[0], "for the user") {
		t.Errorf("sent %q, want the user's answer", got)
	}
}

// Typing shows the moment a scheduled prompt arrives; never for a colleague's.
// The stream stays open so drain's final stopTyping doesn't hide it.
func TestTypingIndicatorOnlyForScheduledEcho(t *testing.T) {
	for origin, want := range map[string]bool{"scheduled": true, "colleague": false} {
		fake := newFakeTelegram(t)
		tr := &Transport{bot: fake.bot(), logger: logx.NewNilLogger(), pumps: map[int64]*chatPump{}}
		p := &chatPump{chatID: 42, sessionID: "s"}
		ch := make(chan client.Event, 1)
		ch <- client.Event{Type: "received_prompt", Origin: origin}
		done := make(chan struct{})
		go func() { tr.drain(context.Background(), p, ch); close(done) }()

		deadline := time.Now().Add(2 * time.Second)
		for want && fake.typingCount() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if !want {
			time.Sleep(300 * time.Millisecond) // give a wrongly started heartbeat time to fire
		}
		if got := fake.typingCount() > 0; got != want {
			t.Errorf("origin %q: typing=%v, want %v", origin, got, want)
		}
		close(ch)
		<-done
	}
}

// End to end through the real server, session and pump: a colleague's prompt to a
// Telegram-bound session runs the turn but sends NOTHING to the chat, the model
// sees the text untouched, and the next typed message is answered in the chat.
func TestColleaguePromptToTelegramSessionRunsQuietEndToEnd(t *testing.T) {
	isolateConfig(t)
	t.Setenv("HOME", t.TempDir())

	var mu sync.Mutex
	var seen []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		last, _ := body.Messages[len(body.Messages)-1].Content.(string)
		mu.Lock()
		seen = append(seen, last)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		ev := func(s string) { w.Write([]byte("data: " + s + "\n\n")); fl.Flush() }
		ev(`{"choices":[{"delta":{"content":"ANSWER to: ` + strings.ReplaceAll(last, `"`, ``) + `"}}]}`)
		ev(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
		ev(`[DONE]`)
	}))
	defer model.Close()
	prov := "tg-quiet-flow"
	if err := agent.NewOpenAIProvider(prov, model.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(prov) })
	a := agent.New(agent.AgentOptions{Store: agentstore.NewInMemoryStore()})
	defer a.Close()
	srv, err := server.Start(a, "", server.ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.RegisterProfile(ProfileName, server.Profile{Directives: []string{Directive}})

	fake := newFakeTelegram(t)
	st, _ := openStore(t.TempDir())
	tr := &Transport{
		api: client.New(srv.Addr()), bot: fake.bot(), store: st, srv: srv,
		logger: logx.NewNilLogger(), model: prov + "/m", cwd: st.cwd, pumps: map[int64]*chatPump{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pump, err := tr.pumpFor(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}

	c := client.New(srv.Addr())
	out, err := c.Ask(pump.sessionID, "what is the status?", 20*time.Second, client.WithOrigin("colleague"), client.WithoutQueue())
	if err != nil || !strings.Contains(out, "ANSWER to: what is the status?") {
		t.Fatalf("the colleague's answer: %q %v", out, err)
	}
	time.Sleep(300 * time.Millisecond) // let the pump drain turn_end
	if got := fake.sent(); len(got) != 0 {
		t.Fatalf("Telegram received %q: a colleague's answer must not be sent", got)
	}
	if fake.typingCount() != 0 {
		t.Errorf("typing shown %d times for a colleague's prompt", fake.typingCount())
	}
	mu.Lock()
	if len(seen) != 1 || seen[0] != "what is the status?" {
		t.Errorf("the model saw %q: the text must arrive untouched, with no origin", seen)
	}
	mu.Unlock()

	if _, err := c.SendPrompt(pump.sessionID, "hello", client.WithOrigin(ProfileName)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(fake.sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := fake.sent()
	if len(got) != 1 || !strings.Contains(got[0], "ANSWER") || !strings.Contains(got[0], "hello") {
		t.Fatalf("Telegram received %q, want exactly the answer to the typed message", got)
	}
}
