package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
)

// Real servers, real agents, real registry — the colleague tools driven the way a
// model drives them. Two servers share one registry (one HOME) and one fake model.
//
// sharedServer sets HOME per call, so the SECOND server is started by hand over
// the first's registry via the same settings store.

// selectiveLLM is an OpenAI-compatible model that holds a turn open ONLY for a
// prompt containing "long task" (the user's), and answers every other one at once.
// fakeLLM's gate blocks every request, which cannot model "one session busy, the
// other idle".
type selectiveLLM struct {
	gate chan struct{}
	mu   sync.Mutex
	seen []string
}

func (l *selectiveLLM) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
		return
	}
	var body struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	last := ""
	if n := len(body.Messages); n > 0 {
		if s, ok := body.Messages[n-1].Content.(string); ok {
			last = s
		}
	}
	l.mu.Lock()
	l.seen = append(l.seen, last)
	l.mu.Unlock()
	if strings.Contains(last, "long task") {
		<-l.gate
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
	fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (l *selectiveLLM) seenPrompts() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

// realColleagueServer starts a server over an agent whose model is llm, returning it
// and a "provider/model" to create sessions with.
func realColleagueServer(t *testing.T, llm *selectiveLLM) (*Server, string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(llm.handler))
	t.Cleanup(ts.Close)
	prov := "coll-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if err := agent.NewOpenAIProvider(prov, ts.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(prov) })
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, prov + "/m"
}

func realColleagues(t *testing.T, llmB *selectiveLLM) (callerTools func(name string) tools.Tool, b *Server, bName, bModel string) {
	t.Helper()
	srvB, modelB := realColleagueServer(t, llmB)
	// Serve registers the instance from its own goroutine, so wait for it to show up.
	var name string
	deadline := time.Now().Add(5 * time.Second)
	for name == "" && time.Now().Before(deadline) {
		insts, _ := ListInstances()
		for n, i := range insts {
			if i.URL == "http://"+srvB.Addr() {
				name = n
			}
		}
		if name == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if name == "" {
		t.Fatalf("server B never registered itself")
	}
	dir := func() map[string]tools.Colleague {
		out := map[string]tools.Colleague{}
		cur, _ := ListInstances()
		for n, i := range cur {
			// the "caller" is a different process than B: give B's entry another PID
			// so ColleagueList does not mistake it for the caller itself.
			out[n] = tools.Colleague{Version: i.Version, Transport: i.Transport, URL: i.URL, CWD: i.CWD, PID: os.Getpid() + 1}
		}
		return out
	}
	return func(which string) tools.Tool {
		if which == "list" {
			return tools.ColleagueList(dir)
		}
		return tools.ColleagueAsk(dir)
	}, srvB, name, modelB
}

// runTool executes a colleague tool the way a model's turn would, but with a hard
// deadline: a call that should have been answered or refused at once and is instead
// parked behind a running turn (the exact bug a no-queue prompt prevents) must fail
// the test in seconds, not hang it for the tool's own two-minute default.
func runTool(t *testing.T, tool tools.Tool, in map[string]any) (string, error) {
	t.Helper()
	b, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	out, err := tool.Execute(ctx, b)
	if ctx.Err() != nil {
		t.Fatalf("the tool call did not return within 6s (it is waiting on a running turn instead of answering or refusing at once): %q %v", out, err)
	}
	return out, err
}

// The whole feature, against the real thing: list shows B's active sessions with
// their busy state; ask goes straight to one, tagged as a colleague, with no resume
// and no new session; a busy one is refused (an error) and nothing reaches it.
func TestColleagueSessionsEndToEnd(t *testing.T) {
	llm := &selectiveLLM{gate: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(llm.gate) }) }
	tool, srvB, bName, modelB := realColleagues(t, llm)
	// Registered AFTER the servers' own cleanups, so it runs BEFORE them (t.Cleanup is
	// LIFO): a failing test must open the gate first, because closing the model's HTTP
	// server waits for the handler that is blocked on it — registered first, it would
	// run last and deadlock the teardown.
	t.Cleanup(release)
	cb := client.New(srvB.Addr())

	// B has two active sessions: one the user is working in, one idle.
	idle, err := cb.CreateSession(modelB, "/proj/idle", "Idle session")
	if err != nil {
		t.Fatal(err)
	}
	working, err := cb.CreateSession(modelB, "/proj/work", "User's session")
	if err != nil {
		t.Fatal(err)
	}
	// the user starts a long turn in the working session
	if _, err := cb.SendPrompt(working.ID, "the user's long task"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		act, _ := cb.ListActiveSessions("")
		busy := false
		for _, a := range act {
			if a.ID == working.ID && a.Busy {
				busy = true
			}
		}
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the working session never became busy")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 1. ColleagueList: what a model sees.
	out, err := runTool(t, tool("list"), map[string]any{})
	if err != nil {
		t.Fatalf("ColleagueList: %v\n%s", err, out)
	}
	var entries []struct {
		Name        string `json:"name"`
		Environment string `json:"environment"`
		Sessions    []struct {
			ID, Name, CWD, Model string
			Busy                 bool
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("list output is not JSON: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0].Name != bName || entries[0].Environment != "server" || len(entries[0].Sessions) != 2 {
		t.Fatalf("entries = %+v\n%s", entries, out)
	}
	busyOf := map[string]bool{}
	for _, s := range entries[0].Sessions {
		busyOf[s.ID] = s.Busy
	}
	if busyOf[working.ID] != true || busyOf[idle.ID] != false {
		t.Errorf("busy flags = %v, want working=true idle=false", busyOf)
	}

	// 2. ColleagueAsk to the IDLE session: answered directly, no resume, no new
	// session, left open, stored with the colleague origin.
	sessionsBefore, _ := cb.ListSessions("")
	reply, err := runTool(t, tool("ask"), map[string]any{"colleague": bName, "session": idle.ID, "prompt": "hello idle one"})
	if err != nil || !strings.Contains(reply, "ok") {
		// the fake model's reply is "ok"
		t.Fatalf("ask idle: %q %v", reply, err)
	}
	sessionsAfter, _ := cb.ListSessions("")
	if len(sessionsAfter) != len(sessionsBefore) {
		t.Errorf("sessions %d → %d: talking to an active session must not create one", len(sessionsBefore), len(sessionsAfter))
	}
	act, _ := cb.ListActiveSessions("")
	stillOpen := false
	for _, a := range act {
		if a.ID == idle.ID {
			stillOpen = true
		}
	}
	if !stillOpen {
		t.Error("the colleague's session was closed by the ask")
	}
	msgs, _ := cb.GetMessages(idle.ID)
	if len(msgs) < 2 || msgs[0].Parts[0].Text != "hello idle one" || msgs[0].Meta == nil || msgs[0].Meta.Origin != "colleague" {
		t.Errorf("history = %+v, want the prompt stored with origin colleague and the text untouched", msgs)
	}

	// 3. ColleagueAsk to the BUSY session: refused at once, as an error; nothing is
	// queued, nothing is stored, the user's conversation is untouched.
	start := time.Now()
	reply, err = runTool(t, tool("ask"), map[string]any{"colleague": bName, "session": working.ID, "prompt": "interrupting question"})
	if err == nil {
		t.Fatalf("a busy session must be an error, got %q", reply)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the busy refusal took %v", time.Since(start))
	}
	if !strings.Contains(reply, "busy") || !strings.Contains(reply, "NOT sent") {
		t.Errorf("the model was told %q", reply)
	}
	release() // the user's turn finishes
	deadline = time.Now().Add(5 * time.Second)
	for {
		a2, _ := cb.ListActiveSessions("")
		idleAgain := false
		for _, a := range a2 {
			if a.ID == working.ID && !a.Busy {
				idleAgain = true
			}
		}
		if idleAgain {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never idle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, p := range llm.seenPrompts() {
		if strings.Contains(p, "interrupting question") {
			t.Fatalf("the model was asked the refused prompt: %v", llm.seenPrompts())
		}
	}
	wm, _ := cb.GetMessages(working.ID)
	for _, m := range wm {
		if len(m.Parts) > 0 && strings.Contains(m.Parts[0].Text, "interrupting question") {
			t.Fatalf("a refused colleague prompt landed in the user's conversation: %+v", wm)
		}
	}

	// 4. A session that is not active: resumed for the question, closed again.
	cb.CloseSession(idle.ID)
	reply, err = runTool(t, tool("ask"), map[string]any{"colleague": bName, "session": idle.ID, "prompt": "still there?"})
	if err != nil || !strings.Contains(reply, idle.ID) || !strings.Contains(reply, "closed") {
		t.Errorf("a closed session: %q %v", reply, err)
	}
	act, _ = cb.ListActiveSessions("")
	for _, a := range act {
		if a.ID == idle.ID {
			t.Error("the session the tool resumed was left open")
		}
	}
	msgs, _ = cb.GetMessages(idle.ID)
	if len(msgs) < 4 {
		t.Errorf("history after the resumed question = %d messages, want the earlier turn kept (>=4)", len(msgs))
	}

	// 5. No session: a new conversation, closed after the answer, id returned.
	sessionsBefore, _ = cb.ListSessions("")
	reply, err = runTool(t, tool("ask"), map[string]any{"colleague": bName, "prompt": "a fresh question"})
	if err != nil {
		t.Fatalf("new conversation: %q %v", reply, err)
	}
	newID := extractSessionIDFromReply(reply)
	if newID == "" {
		t.Fatalf("no session id in the reply: %q", reply)
	}
	sessionsAfter, _ = cb.ListSessions("")
	if len(sessionsAfter) != len(sessionsBefore)+1 {
		t.Errorf("sessions %d → %d, want exactly one new", len(sessionsBefore), len(sessionsAfter))
	}
	act, _ = cb.ListActiveSessions("")
	for _, a := range act {
		if a.ID == newID {
			t.Error("the new conversation was left open")
		}
	}

	// 6. Continue it by id: same history, picked up where it left off, closed again.
	reply, err = runTool(t, tool("ask"), map[string]any{"colleague": bName, "session": newID, "prompt": "and a follow-up"})
	if err != nil || !strings.Contains(reply, newID) {
		t.Fatalf("continue: %q %v", reply, err)
	}
	msgs, _ = cb.GetMessages(newID)
	if len(msgs) != 4 {
		t.Errorf("continued conversation has %d messages, want 4 (two turns)", len(msgs))
	}

	// 7. A nonexistent id: not found, nothing created.
	sessionsBefore, _ = cb.ListSessions("")
	reply, err = runTool(t, tool("ask"), map[string]any{"colleague": bName, "session": "does-not-exist", "prompt": "hi"})
	if err == nil || !strings.Contains(strings.ToLower(reply), "not found") {
		t.Errorf("unknown id: %q %v", reply, err)
	}
	sessionsAfter, _ = cb.ListSessions("")
	if len(sessionsAfter) != len(sessionsBefore) {
		t.Errorf("an unknown id created a session (%d → %d)", len(sessionsBefore), len(sessionsAfter))
	}
}

func extractSessionIDFromReply(reply string) string {
	const marker = "[colleague session: "
	i := strings.Index(reply, marker)
	if i < 0 {
		return ""
	}
	rest := reply[i+len(marker):]
	j := strings.Index(rest, " ")
	if j < 0 {
		return ""
	}
	return rest[:j]
}
