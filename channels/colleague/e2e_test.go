package colleague

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/schedule"
	"github.com/gurcuff91/harness/agent/store"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// scriptedModel is an OpenAI-compatible endpoint whose answers depend on the
// LAST user message: a prompt telling it to ask a colleague makes it call
// ColleagueAsk; a colleague's question gets "ANSWER: <question>"; a reply gets
// "THANKS". It counts the requests, so a loop would show.
type scriptedModel struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests int
}

func newScriptedModel(t *testing.T) *scriptedModel {
	m := &scriptedModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
			return
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.requests++
		m.mu.Unlock()
		last, lastRole := "", ""
		for _, msg := range req.Messages {
			lastRole = msg.Role
			if s, ok := msg.Content.(string); ok {
				last = s
			} else if parts, ok := msg.Content.([]any); ok {
				for _, p := range parts {
					if pm, ok := p.(map[string]any); ok && pm["type"] == "text" {
						last, _ = pm["text"].(string)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta string) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": json.RawMessage(delta)}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		text := func(s string) {
			b, _ := json.Marshal(map[string]string{"content": s})
			chunk(string(b))
		}
		switch {
		case lastRole == "tool" && strings.Contains(fmt.Sprint(req.Messages), "use a tool"):
			text("FINAL AFTER TOOL")
		case lastRole == "tool":
			text("asked") // after ColleagueAsk returned: end the turn
		case strings.HasPrefix(last, "ASK "): // "ASK <to>: <question>"
			to, q, _ := strings.Cut(strings.TrimPrefix(last, "ASK "), ": ")
			args, _ := json.Marshal(map[string]string{"colleague": to, "prompt": q})
			call, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": "ColleagueAsk", "arguments": string(args)}}}})
			chunk(string(call))
		case lastRole == "tool" && strings.Contains(fmt.Sprint(req.Messages), "SCHEDULE "):
			text("scheduled it")
		case strings.Contains(last, "SCHEDULE "): // "SCHEDULE <slug>" in a colleague's question
			slug := strings.Fields(last[strings.Index(last, "SCHEDULE ")+9:])[0]
			args, _ := json.Marshal(map[string]string{"slug": slug, "cron": "@daily", "prompt": "REPORT STATUS"})
			call, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "s1", "type": "function",
				"function": map[string]any{"name": "Schedule", "arguments": string(args)}}}})
			chunk(string(call))
		case strings.Contains(last, "REPORT STATUS"):
			text("STATUS IS GREEN")
		case strings.Contains(last, "reply>"):
			text("THANKS")
		case strings.HasPrefix(last, "<colleague from="):
			body := strings.TrimSuffix(last[strings.Index(last, ">\n")+2:], "\n</colleague>")
			if strings.Contains(body, "use a tool") {
				// commentary, then a tool: the commentary is not the answer
				b, _ := json.Marshal(map[string]any{"content": "LET ME CHECK", "tool_calls": []any{map[string]any{"index": 0, "id": "b1", "type": "function",
					"function": map[string]any{"name": "Bash", "arguments": `{"command":"true"}`}}}})
				chunk(string(b))
				break
			}
			text("ANSWER: " + body)
		default:
			text("ok")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *scriptedModel) count() int { m.mu.Lock(); defer m.mu.Unlock(); return m.requests }

// eventLog collects a session's events (Subscribe is single-handler: this is it).
type eventLog struct {
	mu sync.Mutex
	ev []types.Event
}

func (l *eventLog) add(e types.Event) { l.mu.Lock(); l.ev = append(l.ev, e); l.mu.Unlock() }
func (l *eventLog) prompts() []types.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []types.Event
	for _, e := range l.ev {
		if e.Type == types.EventReceivedPrompt || e.Type == types.EventFollowUpStart {
			out = append(out, e)
		}
	}
	return out
}

// Two sessions on this machine: scorpion asks raiden, raiden answers
// automatically, scorpion receives the answer as a reply — and nothing loops.
func TestTwoColleaguesTalk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	model := newScriptedModel(t)
	if err := admin.RegisterOpenAI("scripted", model.srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("scripted") })

	ch := New(WithRunDir(shortDir(t)))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{ch}})
	defer a.Close()
	open := func() (*agent.Session, *eventLog) {
		s, err := a.NewSession(t.TempDir(), "scripted/m") // registering the channel makes it a colleague
		if err != nil {
			t.Fatal(err)
		}
		l := &eventLog{}
		s.Subscribe(l.add)
		return s, l
	}
	scorpion, sl := open()
	raiden, rl := open()
	sID, rID := colleagueID(t, scorpion), colleagueID(t, raiden)

	sentBefore := repliesSent.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := scorpion.PromptAndWait(ctx, "ASK "+rID+": what is your module path?"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "scorpion receives raiden's reply", func() bool {
		for _, p := range sl.prompts() {
			if p.Origin == "colleague:"+rID+"#reply" {
				return true
			}
		}
		return false
	})
	// raiden got the question from scorpion, with scorpion as origin
	rp := rl.prompts()
	if len(rp) != 1 || rp[0].Origin != "colleague:"+sID || !strings.Contains(rp[0].Output, "what is your module path?") {
		t.Fatalf("raiden's prompts = %+v", rp)
	}
	// the reply carries raiden's final answer
	var echo string
	for _, p := range sl.prompts() {
		if p.Origin == "colleague:"+rID+"#reply" {
			echo = p.Output
		}
	}
	if echo != "colleague:"+rID+" reply (1 line)" {
		t.Errorf("the reply is echoed as %q, want the one-line display", echo)
	}
	reply := storedReplyText(scorpion, "colleague:"+rID+"#reply")
	if !strings.Contains(reply, `<colleague from="`+rID+`" reply>`) || !strings.Contains(reply, "ANSWER: what is your module path?") {
		t.Errorf("reply = %q", reply)
	}
	// No loop: once scorpion's reply turn has ended (its THANKS), give it time
	// and check nothing more arrives on either side.
	eventually(t, "scorpion's reply turn ends", func() bool {
		sl.mu.Lock()
		defer sl.mu.Unlock()
		ends := 0
		for _, e := range sl.ev {
			if e.Type == types.EventTurnEnd {
				ends++
			}
		}
		return ends >= 2 // the ASK turn and the reply turn
	})
	eventually(t, "raiden's turn ends", func() bool {
		rl.mu.Lock()
		defer rl.mu.Unlock()
		for _, e := range rl.ev {
			if e.Type == types.EventTurnEnd {
				return true
			}
		}
		return false
	})
	settled := model.count()
	if n := repliesSent.Load() - sentBefore; n != 1 {
		t.Errorf("%d replies sent, want exactly 1 (raiden's): a reply's turn must send nothing back", n)
	}
	if settled != 4 {
		t.Errorf("model calls = %d, want 4 (scorpion asks, sees the tool result; raiden answers; scorpion reads the reply)", settled)
	}
	time.Sleep(300 * time.Millisecond)
	if model.count() != settled || len(rl.prompts()) != 1 || len(sl.prompts()) != 2 {
		t.Errorf("messages kept flowing: %d→%d model calls, raiden %d prompts, scorpion %d", settled, model.count(), len(rl.prompts()), len(sl.prompts()))
	}
}

// A colleague that left loses the reply: nothing fails, nothing is delivered.
func TestAReplyToAColleagueThatLeftIsLost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	model := newScriptedModel(t)
	if err := admin.RegisterOpenAI("scripted2", model.srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("scripted2") })
	ch := New(WithRunDir(shortDir(t)))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{ch}})
	defer a.Close()
	raiden, err := a.NewSession(t.TempDir(), "scripted2/m")
	if err != nil {
		t.Fatal(err)
	}
	rID := colleagueID(t, raiden)
	l := &eventLog{}
	raiden.Subscribe(l.add)
	// a "colleague" that asks and is gone before the answer
	if err := send(context.Background(), ch.socketPath(rID), message{Kind: kindAsk, From: "ghost-one", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "raiden answers", func() bool { return model.count() >= 1 && len(l.prompts()) == 1 })
	time.Sleep(200 * time.Millisecond)
	if model.count() != 1 {
		t.Errorf("model calls = %d, want 1", model.count())
	}
}

func TestAskerOf(t *testing.T) {
	for origin, want := range map[string]string{
		"colleague:scorpion-hero":       "scorpion-hero",
		"colleague:scorpion-hero#reply": "",
		"colleague:../x":                "",
		"slack:C1":                      "",
		"user":                          "",
		"":                              "",
	} {
		if got := askerOf(origin); got != want {
			t.Errorf("askerOf(%q) = %q, want %q", origin, got, want)
		}
	}
}

// The answer is the turn's FINAL text: commentary written before a tool call
// is not sent back.
func TestTheReplyIsTheFinalText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	model := newScriptedModel(t)
	if err := admin.RegisterOpenAI("scripted3", model.srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("scripted3") })
	ch := New(WithRunDir(shortDir(t)))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{ch}})
	defer a.Close()
	raiden, err := a.NewSession(t.TempDir(), "scripted3/m")
	if err != nil {
		t.Fatal(err)
	}
	rID := colleagueID(t, raiden)
	var got inboxRec
	stop, err := ch.Listen(context.Background(), "asker-one", got.deliver) // a bare listener as the asker
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := send(context.Background(), ch.socketPath(rID), message{Kind: kindAsk, From: "asker-one", Text: "please use a tool"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the reply", func() bool { return len(got.all()) == 1 })
	r := got.all()[0].Text
	if !strings.Contains(r, "FINAL AFTER TOOL") || strings.Contains(r, "LET ME CHECK") {
		t.Errorf("reply = %q", r)
	}
}

// An automatic id never lands on one a live session already holds: from a
// pool of four ids with three taken, only the free one ever comes back.
func TestAutoIDAvoidsTakenIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	pool := []string{"a-1", "a-2", "a-3", "a-free"}
	n := 0
	old := randomID
	randomID = func() string { n++; return pool[n%len(pool)] }
	t.Cleanup(func() { randomID = old })
	sm := config.GetSettingsManager()
	for _, id := range pool[:3] {
		_, _ = sm.ClaimChannel(config.ChannelClaim{Type: Type, ID: id, Session: "s-" + id, PID: os.Getpid()}, func(config.ChannelClaim) bool { return true })
	}
	ch := New(WithRunDir(shortDir(t)))
	for i := 0; i < 20; i++ {
		id, err := ch.Resolve(context.Background(), autoID)
		if err != nil || id != "a-free" {
			t.Fatalf("Resolve = %q, %v; want the one free id", id, err)
		}
	}
	// all taken: an error, never a taken id
	_, _ = sm.ClaimChannel(config.ChannelClaim{Type: Type, ID: "a-free", Session: "s-free", PID: os.Getpid()}, func(config.ChannelClaim) bool { return true })
	if id, err := ch.Resolve(context.Background(), autoID); err == nil {
		t.Fatalf("every id taken, yet got %q", id)
	}
}

// colleagueID is the id the colleague channel gave a session (every session
// the agent opens gets one: the channel joins it by itself).
func colleagueID(t *testing.T, s *agent.Session) string {
	t.Helper()
	for _, c := range s.Channels() {
		if c.Type == Type {
			return c.ID
		}
	}
	t.Fatal("the session is not a colleague")
	return ""
}

// ColleagueList shows each colleague's session NAME, kept current on rename.
func TestColleagueListShowsTheSessionName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	model := newScriptedModel(t)
	if err := admin.RegisterOpenAI("scripted", model.srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("scripted") })
	ch := New(WithRunDir(shortDir(t)))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{ch}})
	defer a.Close()
	s1, err := a.NewSession(t.TempDir(), "scripted/m")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := a.NewSession(t.TempDir(), "scripted/m")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Rename("refactor del parser"); err != nil {
		t.Fatal(err)
	}
	id1, id2 := colleagueID(t, s1), colleagueID(t, s2)
	var list atools.Tool
	for _, tl := range ch.Tools([]string{id1}) {
		if tl.Def.Name == "ColleagueList" {
			list = tl
		}
	}
	out, err := list.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		ID, Session string
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.ID] = e.Session
	}
	if got[id2] != "refactor del parser" || got[id1] != s1.Name() || s1.Name() == "" {
		t.Fatalf("ColleagueList = %s", out)
	}
}

// listOf runs ColleagueList as session id of channel ch.
func listOf(t *testing.T, ch *Channel, id string) map[string]string {
	t.Helper()
	var list atools.Tool
	for _, tl := range ch.Tools([]string{id}) {
		if tl.Def.Name == "ColleagueList" {
			list = tl
		}
	}
	out, err := list.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		ID      string
		Session *string
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.ID] = "<none>"
		if e.Session != nil {
			got[e.ID] = *e.Session
		}
	}
	return got
}

// Across processes (two agents, two channels, one run dir): the name comes
// from the OTHER colleague over its socket (a ping), live — a rename shows
// at once, with nothing written to the claim.
func TestColleagueListPingsOtherProcessesForTheName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	model := newScriptedModel(t)
	if err := admin.RegisterOpenAI("scripted", model.srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("scripted") })
	run := shortDir(t)
	chA, chB := New(WithRunDir(run)), New(WithRunDir(run))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{chA}})
	defer a.Close()
	b := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{chB}})
	defer b.Close()
	sa, err := a.NewSession(t.TempDir(), "scripted/m")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.NewSession(t.TempDir(), "scripted/m")
	if err != nil {
		t.Fatal(err)
	}
	idA, idB := colleagueID(t, sa), colleagueID(t, sb)
	if err := sb.Rename("refactor del parser"); err != nil {
		t.Fatal(err)
	}
	got := listOf(t, chA, idA)
	if got[idB] != "refactor del parser" || got[idA] != sa.Name() {
		t.Fatalf("ColleagueList = %v", got)
	}
	if err := sb.Rename("otro nombre"); err != nil {
		t.Fatal(err)
	}
	if got := listOf(t, chA, idA); got[idB] != "otro nombre" {
		t.Fatalf("a rename must show at once: %v", got)
	}
	for _, cl := range config.GetSettingsManager().ChannelClaims() {
		raw, _ := json.Marshal(cl)
		if strings.Contains(string(raw), "otro nombre") || strings.Contains(string(raw), "session_name") {
			t.Fatalf("the claim must not carry the name: %s", raw)
		}
	}
}

// A colleague that does not answer ping (an older harness: "unknown message
// kind") is still listed, without a name, and does not slow the list down.
func TestColleagueListWithAnOldColleague(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	run := shortDir(t)
	ch := New(WithRunDir(run))
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	// The old colleague: a socket that answers every message the old way.
	ln, err := net.Listen("unix", ch.socketPath("old-one"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var m message
			_ = readLine(conn, &m)
			respond(conn, fmt.Errorf("unknown message kind %q", m.Kind))
			conn.Close()
		}
	}()
	// Two silent ones: accept and never answer (each bounded by pingTimeout;
	// pinged in parallel, the list takes ~one timeout, not two).
	for _, id := range []string{"silent-one", "silent-two"} {
		l, err := net.Listen("unix", ch.socketPath(id))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				if _, err := l.Accept(); err != nil {
					return
				}
			}
		}()
	}
	for _, id := range []string{"old-one", "silent-one", "silent-two"} {
		if _, err := config.GetSettingsManager().ClaimChannel(config.ChannelClaim{Type: Type, ID: id, Session: "s-" + id, PID: os.Getpid()}, func(config.ChannelClaim) bool { return true }); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	got := listOf(t, ch, "me")
	if got["old-one"] != "<none>" || got["silent-one"] != "<none>" || got["silent-two"] != "<none>" {
		t.Fatalf("got %v", got)
	}
	if d := time.Since(start); d >= 2*pingTimeout {
		t.Errorf("the list took %v; pings run in parallel, bounded by %v", d, pingTimeout)
	}
}

// A colleague asks for something periodic: the schedule created while
// answering its question fires back TO it — the colleague gets the answer as a
// reply each time — and that reply sends nothing back (no loop).
func TestAScheduleAColleagueAskedForAnswersThatColleague(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	model := newScriptedModel(t)
	if err := admin.RegisterOpenAI("scripted", model.srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("scripted") })
	ch := New(WithRunDir(shortDir(t)))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{ch}, EnableScheduler: true})
	defer a.Close()
	open := func() (*agent.Session, *eventLog) {
		s, err := a.NewSession(t.TempDir(), "scripted/m")
		if err != nil {
			t.Fatal(err)
		}
		l := &eventLog{}
		s.Subscribe(l.add)
		return s, l
	}
	scorpion, sl := open()
	raiden, rl := open()
	sID, rID := colleagueID(t, scorpion), colleagueID(t, raiden)
	slug := "tg-" + strings.ReplaceAll(t.Name(), "/", "")[:8]
	t.Cleanup(func() { _, _ = schedStore(t).Delete(slug, scorpion.ID()) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// raiden asks scorpion to schedule something.
	if _, err := raiden.PromptAndWait(ctx, "ASK "+sID+": SCHEDULE "+slug+" every day"); err != nil {
		t.Fatal(err)
	}
	var stored schedule.Schedule
	eventually(t, "the schedule exists with the colleague's origin", func() bool {
		for _, sc := range schedStore(t).List() {
			if sc.Slug == slug && sc.Owner == scorpion.ID() {
				stored = sc
				return true
			}
		}
		return false
	})
	if stored.Origin != "colleague:"+rID {
		t.Fatalf("schedule origin = %q, want colleague:%s", stored.Origin, rID)
	}
	eventually(t, "the scheduling turn is over on both sides", func() bool {
		return !scorpion.IsBusy() && !raiden.IsBusy() && len(rl.prompts()) >= 1
	})
	repliesBefore := len(rl.prompts())

	// It fires: scorpion runs it as raiden's question, and raiden gets the answer.
	a.FireScheduleForTest(stored)
	eventually(t, "raiden receives the scheduled answer", func() bool {
		for _, p := range rl.prompts()[repliesBefore:] {
			if p.Origin == "colleague:"+sID+"#reply" && strings.Contains(storedReplyText(raiden, p.Origin), "STATUS IS GREEN") {
				return true
			}
		}
		return false
	})
	var fired types.Event
	for _, p := range sl.prompts() {
		if strings.HasPrefix(strings.TrimSpace(p.Output), "<scheduled slug=") {
			fired = p
		}
	}
	if fired.Origin != "colleague:"+rID {
		t.Errorf("the fired turn's origin = %q, want the colleague's", fired.Origin)
	}
	// No loop: raiden's reply turn sends nothing back.
	eventually(t, "settled", func() bool { return !scorpion.IsBusy() && !raiden.IsBusy() })
	n := len(sl.prompts())
	time.Sleep(300 * time.Millisecond)
	if len(sl.prompts()) != n {
		t.Errorf("scorpion got more prompts after the reply: a loop")
	}
}

// schedStore opens the schedule store (a view over the process-global settings,
// the same one the agent uses).
func schedStore(t *testing.T) *schedule.Store {
	t.Helper()
	st, err := schedule.Open()
	if err != nil || st == nil {
		t.Fatalf("schedule store: %v", err)
	}
	return st
}

// storedReplyText is the full text of the last stored user message with origin:
// the echo only shows the one-line display, the log keeps everything.
func storedReplyText(s *agent.Session, origin string) string {
	var text string
	for _, m := range s.AllMessages() {
		if m.Role == types.RoleUser && m.Meta != nil && m.Meta.Origin == origin && len(m.Parts) > 0 {
			text = m.Parts[0].Text
		}
	}
	return text
}
