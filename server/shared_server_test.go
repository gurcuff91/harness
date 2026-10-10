package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// fakeLLM is an OpenAI-compatible endpoint that lists one model and answers
// every chat completion with a short streamed reply — enough to drive real
// turns. gate, when non-nil, blocks each completion until it's closed (to
// hold a turn open). seen records each request's tool names + system prompt.
type fakeLLM struct {
	gate chan struct{}
	mu   sync.Mutex
	seen []map[string]any
}

func (f *fakeLLM) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.seen = append(f.seen, body)
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
	fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// sharedServer is ONE running server over an agent with a fake model — the
// layered setup the transports now run on.
func sharedServer(t *testing.T, llm *fakeLLM) (*Server, *agent.Agent, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if llm == nil {
		llm = &fakeLLM{}
	}
	ts := httptest.NewServer(http.HandlerFunc(llm.handler))
	t.Cleanup(ts.Close)
	prov := "shared-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if err := agent.NewOpenAIProvider(prov, ts.URL); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, a, prov + "/m"
}

// collect drains events from ch until a turn_end, returning them.
func collect(t *testing.T, ch <-chan client.Event) []client.Event {
	t.Helper()
	var got []client.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, e)
			if e.Type == "turn_end" {
				return got
			}
		case <-timeout:
			t.Fatalf("no turn_end; got %v", got)
		}
	}
}

func eventOrigin(evs []client.Event) string {
	for _, e := range evs {
		if e.Type == "received_prompt" || e.Type == "follow_up_start" {
			return e.Origin
		}
	}
	return ""
}

func TestStartAddrIsValidImmediately(t *testing.T) {
	srv, _, _ := sharedServer(t, nil)
	if srv.Addr() == "" {
		t.Fatal("Addr() empty right after Start")
	}
	if _, err := client.New(srv.Addr()).GetServerInfo(); err != nil {
		t.Fatalf("server not accepting at Addr(): %v", err)
	}
	if NewServer(nil, ServerOptions{}).Addr() != "" {
		t.Error("an unstarted server must report an empty Addr()")
	}
}

// Two clients (a "transport" and a plain UI client) streaming the SAME session
// on one server both receive every event — no subscriber stealing — and the
// origin each prompt was sent with is echoed to both.
func TestTwoClientsOneSessionBothGetEveryEvent(t *testing.T) {
	srv, _, model := sharedServer(t, nil)
	transport, ui := client.New(srv.Addr()), client.New(srv.Addr())

	sess, err := transport.CreateSession(model, "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evT, err := transport.StreamEvents(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ui.ResumeSession(sess.ID); err != nil { // the UI opens it too
		t.Fatal(err)
	}
	evU, err := ui.StreamEvents(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let both SSE subscriptions register

	if _, err := transport.SendPrompt(sess.ID, "hi", client.WithOrigin("slack")); err != nil {
		t.Fatal(err)
	}
	gotT, gotU := collect(t, evT), collect(t, evU)
	if len(gotT) == 0 || len(gotT) != len(gotU) {
		t.Fatalf("transport got %d events, ui got %d — both must get every event", len(gotT), len(gotU))
	}
	if o1, o2 := eventOrigin(gotT), eventOrigin(gotU); o1 != "slack" || o2 != "slack" {
		t.Errorf("origin = %q / %q, want slack for both", o1, o2)
	}

	// A prompt typed from the UI (caller-defined origin) reaches the transport too.
	if _, err := ui.SendPrompt(sess.ID, "from ui", client.WithOrigin("kaiban")); err != nil {
		t.Fatal(err)
	}
	if o := eventOrigin(collect(t, evT)); o != "kaiban" {
		t.Errorf("transport saw origin %q for the UI's prompt, want kaiban", o)
	}
	if o := eventOrigin(collect(t, evU)); o != "kaiban" {
		t.Errorf("ui saw origin %q, want kaiban", o)
	}
}

// Concurrent resumes of one session never build a second proxy (which would
// steal the event stream), and closing closes it once, for everyone.
func TestConcurrentResumeOneProxyAndSingleClose(t *testing.T) {
	srv, a, model := sharedServer(t, nil)
	c := client.New(srv.Addr())
	sess, err := c.CreateSession(model, "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	c.CloseSession(sess.ID)

	// 64 concurrent opens released at once (the race window is tiny — HTTP
	// round trips alone rarely hit it; without openMu this builds ~10+).
	before := proxiesCreated.Load()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; srv.openSession(sess.ID, nil, nil) }()
	}
	close(start)
	wg.Wait()
	srv.mu.RLock()
	proxies := 0
	for id := range srv.sessions {
		if id == sess.ID {
			proxies++
		}
	}
	live := srv.sessions[sess.ID]
	srv.mu.RUnlock()
	if proxies != 1 || live == nil {
		t.Fatalf("proxies for the session = %d, want exactly 1", proxies)
	}
	if built := proxiesCreated.Load() - before; built != 1 {
		t.Fatalf("64 concurrent opens built %d proxies, want exactly 1 (a second one steals the event stream)", built)
	}
	if got, _ := a.ResumeSession(sess.ID); got != live.session {
		t.Fatal("the agent's live session and the server's proxy diverged")
	}

	if _, err := c.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CloseSession(sess.ID); err == nil {
		t.Error("a second close must report the session as not active")
	}
}

func profileToolNames(s *agent.Session) []string {
	var out []string
	for _, d := range s.Tools() {
		if strings.HasPrefix(d.Name, "Prof") {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return out
}

func profTool(name string) tools.Tool {
	return tools.Tool{Def: types.ToolDef{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

// Profiles: applied only to bound sessions, persisted by name, re-applied to
// any later open (with or without options), skipped while unregistered, and
// unknown names are a 400.
func TestProfilesBindPersistAndApply(t *testing.T) {
	srv, a, model := sharedServer(t, nil)
	srv.RegisterProfile("chat", Profile{
		Directives: []string{"## Chat directive"},
		Tools:      func() []tools.Tool { return []tools.Tool{profTool("ProfChat")} },
	})
	c := client.New(srv.Addr())

	bound, err := c.CreateSession(model, "/p", "", client.WithProfiles("chat"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.CreateSession(model, "/q", "")
	if err != nil {
		t.Fatal(err)
	}
	bs, _ := a.ResumeSession(bound.ID)
	ps, _ := a.ResumeSession(plain.ID)
	if !strings.Contains(bs.SystemPrompt(), "## Chat directive") || !slices.Equal(profileToolNames(bs), []string{"ProfChat"}) {
		t.Fatal("bound session lacks the profile")
	}
	if strings.Contains(ps.SystemPrompt(), "## Chat directive") || len(profileToolNames(ps)) != 0 {
		t.Fatal("unbound session got the profile")
	}
	if got := a.SessionProfiles(bound.ID); !slices.Equal(got, []string{"chat"}) {
		t.Fatalf("persisted binding = %v", got)
	}

	// Reopened by a plain client with NO options: profile re-applied.
	c.CloseSession(bound.ID)
	if _, err := c.ResumeSession(bound.ID); err != nil {
		t.Fatal(err)
	}
	re, _ := a.ResumeSession(bound.ID)
	if !strings.Contains(re.SystemPrompt(), "## Chat directive") {
		t.Fatal("plain reopen must re-apply the persisted profile")
	}

	// Unregistered: skipped entirely on the next open; binding kept.
	srv.UnregisterProfile("chat")
	c.CloseSession(bound.ID)
	c.ResumeSession(bound.ID)
	off, _ := a.ResumeSession(bound.ID)
	if strings.Contains(off.SystemPrompt(), "## Chat directive") || len(profileToolNames(off)) != 0 {
		t.Fatal("an unregistered profile must be skipped")
	}
	if got := a.SessionProfiles(bound.ID); !slices.Equal(got, []string{"chat"}) {
		t.Fatalf("binding must survive unregistration, got %v", got)
	}

	// Unknown name → 400.
	if _, err := c.CreateSession(model, "/p", "", client.WithProfiles("nope")); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("unknown profile = %v, want a 400 unknown-profile error", err)
	}
	if _, err := c.ResumeSession(bound.ID, client.WithProfiles("nope")); err == nil {
		t.Fatal("unknown profile on resume must fail")
	}
}

// Re-resuming an ACTIVE session with a new profile applies it at the next
// turn boundary — never mid-turn.
func TestRebindActiveSessionAppliesAtTurnBoundary(t *testing.T) {
	llm := &fakeLLM{gate: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(llm.gate) }) }
	srv, a, model := sharedServer(t, llm)
	t.Cleanup(release) // never leave the in-flight turn blocked (cleanup waits on it)
	srv.RegisterProfile("late", Profile{
		Directives: []string{"## Late directive"},
		Tools:      func() []tools.Tool { return []tools.Tool{profTool("ProfLate")} },
	})
	c := client.New(srv.Addr())
	sess, err := c.CreateSession(model, "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev, _ := c.StreamEvents(ctx, sess.ID)
	time.Sleep(50 * time.Millisecond)

	c.SendPrompt(sess.ID, "first")
	waitFor := func(pred func() bool) {
		deadline := time.Now().Add(5 * time.Second)
		for !pred() {
			if time.Now().After(deadline) {
				t.Fatal("timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(func() bool { llm.mu.Lock(); defer llm.mu.Unlock(); return len(llm.seen) == 1 }) // turn 1 in flight

	// Mid-turn: bind the profile.
	if _, err := c.ResumeSession(sess.ID, client.WithProfiles("late")); err != nil {
		t.Fatal(err)
	}
	live, _ := a.ResumeSession(sess.ID)
	if strings.Contains(live.SystemPrompt(), "## Late directive") {
		t.Fatal("the profile was applied mid-turn")
	}
	release()
	collect(t, ev)

	c.SendPrompt(sess.ID, "second")
	collect(t, ev)
	if !strings.Contains(live.SystemPrompt(), "## Late directive") || !slices.Equal(profileToolNames(live), []string{"ProfLate"}) {
		t.Fatal("the profile must apply at the next turn boundary")
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.seen) != 2 {
		t.Fatalf("completions = %d, want 2", len(llm.seen))
	}
	sys := fmt.Sprint(llm.seen[1]["messages"])
	if !strings.Contains(sys, "## Late directive") {
		t.Error("the second turn's request must carry the late directive")
	}
	if strings.Contains(fmt.Sprint(llm.seen[0]["messages"]), "## Late directive") {
		t.Error("the first (in-flight) turn's request must not")
	}
}
