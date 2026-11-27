package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/jsonrpc"
)

// sessGated is a fake model whose answers can be held back: while gated, a
// chat completion waits for release() (or for its request to be cancelled).
type sessGated struct {
	mu      sync.Mutex
	gate    chan struct{}
	started chan struct{}
}

func (g *sessGated) hold() {
	g.mu.Lock()
	g.gate = make(chan struct{})
	g.started = make(chan struct{}, 16)
	g.mu.Unlock()
}

func (g *sessGated) release() {
	g.mu.Lock()
	if g.gate != nil {
		close(g.gate)
		g.gate = nil
	}
	g.mu.Unlock()
}

func (g *sessGated) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Write([]byte(`{"data":[{"id":"m"},{"id":"m2"}]}`))
		return
	}
	g.mu.Lock()
	gate, started := g.gate, g.started
	g.mu.Unlock()
	if gate != nil {
		started <- struct{}{}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
	fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func sessRegister(t *testing.T, g *sessGated) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(g.handler))
	t.Cleanup(ts.Close)
	t.Cleanup(g.release)
	prov := "rpc-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	if err := admin.RegisterOpenAI(prov, ts.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider(prov) })
	return prov + "/m"
}

// sessOpen starts a connection with one open session on a gated model.
func sessOpen(t *testing.T, cfg Config) (*client, *sessGated, agent.SessionDetail) {
	t.Helper()
	g := &sessGated{}
	model := sessRegister(t, g)
	c := start(t)
	c.initialize(cfg)
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
	return c, g, d
}

func TestSessionPromptFullTurn(t *testing.T) {
	c, _, d := sessOpen(t, memStoreConfig())
	var r SessionPromptResult
	c.must("session/prompt", SessionPromptParams{ID: d.ID, Text: "hi", Origin: "kaiban", DisplayText: "shown"}, &r)
	if r.Status != "started" {
		t.Fatalf("status = %q", r.Status)
	}
	evs := c.collectUntil("turn_end")
	var types []string
	var text string
	for _, e := range evs {
		types = append(types, e["type"].(string))
		if e["type"] == "text" {
			text += e["delta"].(string)
		}
		if e["type"] == "received_prompt" && e["origin"] != "kaiban" {
			t.Errorf("origin not echoed: %v", e)
		}
	}
	joined := " " + strings.Join(types, " ") + " "
	if text != "ok" || !strings.Contains(joined, " received_prompt ") || !strings.Contains(joined, " turn_start ") || types[len(types)-1] != "turn_end" {
		t.Fatalf("events = %v text = %q", types, text)
	}
	var ask SessionAskResult
	c.must("session/ask", SessionPromptParams{ID: d.ID, Text: "again"}, &ask)
	if ask.Text != "ok" {
		t.Fatalf("ask = %+v", ask)
	}
}

func TestSessionPromptValidation(t *testing.T) {
	c, _, d := sessOpen(t, memStoreConfig())
	c.fail("session/prompt", SessionPromptParams{ID: d.ID}, CodeInvalid, "invalid")
	c.fail("session/ask", SessionPromptParams{ID: d.ID}, CodeInvalid, "invalid")
	c.fail("session/prompt", SessionPromptParams{ID: d.ID, Text: "x", Images: []imageData{{MimeType: "image/png", Base64: "AAAA"}}}, CodeInvalid, "invalid")
}

func TestSessionPromptQueuedWhileBusy(t *testing.T) {
	c, g, d := sessOpen(t, memStoreConfig())
	g.hold()
	var r SessionPromptResult
	c.must("session/prompt", SessionPromptParams{ID: d.ID, Text: "one"}, &r)
	<-g.started
	c.must("session/prompt", SessionPromptParams{ID: d.ID, Text: "two"}, &r)
	if r.Status != "queued" {
		t.Fatalf("status = %q, want queued", r.Status)
	}
	var info SessionInfoResult
	c.must("session/info", SessionIDParams{ID: d.ID}, &info)
	if !info.Busy || info.QueueDepth != 1 {
		t.Fatalf("info = busy %v queue %d", info.Busy, info.QueueDepth)
	}
	// compact and reset refuse while a turn runs
	c.fail("session/compact", SessionIDParams{ID: d.ID}, CodeConflict, "conflict")
	c.fail("session/reset", SessionIDParams{ID: d.ID}, CodeConflict, "conflict")
	g.release()
	c.collectUntil("turn_end")
	c.collectUntil("turn_end")
}

func TestSessionStopInterruptsTurnAndUnblocksAsk(t *testing.T) {
	c, g, d := sessOpen(t, memStoreConfig())
	g.hold()
	type res struct {
		out SessionAskResult
		je  *jsonrpc.Error
	}
	got := make(chan res, 1)
	go func() {
		raw, je := c.call("session/ask", SessionPromptParams{ID: d.ID, Text: "slow"})
		var r res
		r.je = je
		if je == nil {
			_ = jsonUnmarshal(raw, &r.out)
		}
		got <- r
	}()
	<-g.started
	select {
	case <-got:
		t.Fatal("ask returned before the turn ended")
	case <-time.After(100 * time.Millisecond):
	}
	c.must("session/stop", SessionIDParams{ID: d.ID}, nil)
	select {
	case r := <-got:
		if r.je != nil || r.out.Text != "" {
			t.Fatalf("ask after stop = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask not unblocked by session/stop")
	}
	c.waitEvent("stop")
}

func TestSessionAskReturnsWhenConnectionCloses(t *testing.T) {
	c, g, d := sessOpen(t, memStoreConfig())
	g.hold()
	go c.call("session/ask", SessionPromptParams{ID: d.ID, Text: "slow"})
	<-g.started
	// stdin closing (cleanup) must end Run promptly even with an ask pending.
	c.inW.Close()
	select {
	case err := <-c.done:
		c.done <- err // the cleanup waits for it too
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return with a pending ask")
	}
}

func TestSessionCompactAndReset(t *testing.T) {
	c, _, d := sessOpen(t, memStoreConfig())
	c.must("session/ask", SessionPromptParams{ID: d.ID, Text: "hello"}, nil)
	var m SessionMessagesResult
	c.must("session/messages", SessionMessagesParams{ID: d.ID}, &m)
	if m.Total != 2 {
		t.Fatalf("total before compact = %d", m.Total)
	}
	c.must("session/compact", SessionIDParams{ID: d.ID}, nil)
	c.waitEvent("compact_end")
	c.must("session/reset", SessionIDParams{ID: d.ID}, nil)
	c.must("session/messages", SessionMessagesParams{ID: d.ID}, &m)
	if m.Total != 0 || m.Messages == nil || len(m.Messages) != 0 {
		t.Fatalf("after reset = %+v", m)
	}
}

func TestSessionSettersReturnDetailAndPersist(t *testing.T) {
	c, _, d := sessOpen(t, memStoreConfig())
	var det agent.SessionDetail
	c.must("session/rename", SessionRenameParams{ID: d.ID, Name: "renamed"}, &det)
	if det.Name != "renamed" {
		t.Fatalf("rename: %+v", det)
	}
	c.must("session/switch_thinking", SessionSwitchThinkingParams{ID: d.ID, Level: "high"}, &det)
	if det.Thinking != "high" {
		t.Fatalf("thinking: %+v", det)
	}
	c.must("session/set_max_iterations", SessionSetMaxIterationsParams{ID: d.ID, N: 7}, &det)
	if det.MaxIterations != 7 {
		t.Fatalf("max iterations: %+v", det)
	}
	newModel := strings.TrimSuffix(d.Model, "/m") + "/m2"
	c.must("session/switch_model", SessionSwitchModelParams{ID: d.ID, Model: newModel}, &det)
	if det.Model != newModel {
		t.Fatalf("model: %+v", det)
	}
	var info SessionInfoResult
	c.must("session/info", SessionIDParams{ID: d.ID}, &info)
	if info.Session.Name != "renamed" || info.Session.Thinking != "high" || info.Session.MaxIterations != 7 || info.Session.Model != newModel {
		t.Fatalf("info does not show the changes: %+v", info.Session)
	}
	// persisted: close and resume
	c.must("session/close", SessionIDParams{ID: d.ID}, nil)
	var again agent.SessionDetail
	c.must("session/resume", SessionResumeParams{ID: d.ID}, &again)
	if again.Name != "renamed" || again.Thinking != "high" || again.MaxIterations != 7 || again.Model != newModel {
		t.Fatalf("resumed: %+v", again)
	}
}

func TestSessionSetterValidation(t *testing.T) {
	c, _, d := sessOpen(t, memStoreConfig())
	c.fail("session/switch_thinking", SessionSwitchThinkingParams{ID: d.ID, Level: "ultra"}, CodeInvalid, "unprocessable")
	c.fail("session/switch_thinking", SessionSwitchThinkingParams{ID: d.ID}, CodeInvalid, "invalid")
	c.fail("session/set_max_iterations", SessionSetMaxIterationsParams{ID: d.ID, N: 0}, CodeInvalid, "unprocessable")
	c.fail("session/set_max_iterations", SessionSetMaxIterationsParams{ID: d.ID, N: 5000}, CodeInvalid, "unprocessable")
	c.fail("session/rename", SessionRenameParams{ID: d.ID}, CodeInvalid, "invalid")
	c.fail("session/switch_model", SessionSwitchModelParams{ID: d.ID}, CodeInvalid, "invalid")
	c.fail("session/switch_model", SessionSwitchModelParams{ID: d.ID, Model: "nope/none"}, CodeNotFound, "provider_not_found")
}

func TestSessionMessagesWindows(t *testing.T) {
	c, _, d := sessOpen(t, memStoreConfig())
	for i := 0; i < 3; i++ {
		c.must("session/ask", SessionPromptParams{ID: d.ID, Text: fmt.Sprint("q", i)}, nil)
	}
	var m SessionMessagesResult
	c.must("session/messages", SessionMessagesParams{ID: d.ID}, &m)
	if m.Total != 6 || m.From != 0 || len(m.Messages) != 6 {
		t.Fatalf("all = from %d total %d n %d", m.From, m.Total, len(m.Messages))
	}
	from := 4
	c.must("session/messages", SessionMessagesParams{ID: d.ID, From: &from}, &m)
	if m.Total != 6 || m.From != 4 || len(m.Messages) != 2 {
		t.Fatalf("from = from %d total %d n %d", m.From, m.Total, len(m.Messages))
	}
	c.must("session/compact", SessionIDParams{ID: d.ID}, nil)
	c.must("session/ask", SessionPromptParams{ID: d.ID, Text: "after"}, nil)
	k := 1
	c.must("session/messages", SessionMessagesParams{ID: d.ID, Compactions: &k}, &m)
	if m.From == 0 || len(m.Messages) != m.Total-m.From {
		t.Fatalf("compactions window = from %d total %d n %d", m.From, m.Total, len(m.Messages))
	}
	zero, neg := 0, -1
	c.fail("session/messages", SessionMessagesParams{ID: d.ID, From: &from, Compactions: &k}, CodeInvalid, "invalid")
	c.fail("session/messages", SessionMessagesParams{ID: d.ID, Compactions: &zero}, CodeInvalid, "invalid")
	c.fail("session/messages", SessionMessagesParams{ID: d.ID, From: &neg}, CodeInvalid, "invalid")
}

func TestSessionReadShapes(t *testing.T) {
	c, _, d := sessOpen(t, memStoreConfig())
	var info SessionInfoResult
	c.must("session/info", SessionIDParams{ID: d.ID}, &info)
	if info.Version == "" || info.Session.ID != d.ID || info.OS == "" || info.Arch == "" || info.Busy || info.QueueDepth != 0 {
		t.Fatalf("info = %+v", info)
	}
	raw, je := c.call("session/info", SessionIDParams{ID: d.ID})
	if je != nil {
		t.Fatal(je)
	}
	for _, k := range []string{`"version"`, `"session"`, `"busy"`, `"queue_depth"`, `"mcp_connected"`, `"schedule_count"`, `"os"`, `"arch"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("info lacks %s: %s", k, raw)
		}
	}
	var cb agent.ContextBreakdown
	c.must("session/context", SessionIDParams{ID: d.ID}, &cb)
	if cb.System == 0 || cb.ContextWindow == 0 {
		t.Fatalf("context = %+v", cb)
	}
	var sp SessionSyspromptResult
	c.must("session/sysprompt", SessionIDParams{ID: d.ID}, &sp)
	if sp.Text == "" {
		t.Fatal("empty system prompt")
	}
	var tl SessionToolsResult
	c.must("session/tools", SessionIDParams{ID: d.ID}, &tl)
	found := false
	for _, td := range tl.Tools {
		found = found || td.Name == "Bash"
	}
	if !found {
		t.Fatalf("tools = %+v", tl.Tools)
	}
}

func TestSessionExecCommand(t *testing.T) {
	g := &sessGated{}
	model := sessRegister(t, g)
	c := start(t)
	c.initialize(Config{Store: &KindConfig{Kind: "ephemeral"}, Resources: &ResourcesConfig{Kind: "file"}}) // skills are only discovered when asked for
	cwd := t.TempDir()
	skill := filepath.Join(cwd, ".harness", "agent", "skills", "greet")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: greet\ndescription: says hi\n---\nSAY-HELLO-BODY\n"), 0o644)
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: cwd, Model: model}, &d)

	var r SessionExecCommandResult
	c.must("session/exec_command", SessionExecCommandParams{ID: d.ID, Command: "skill:greet", Params: map[string]any{"prompt": "to Ana"}}, &r)
	if r.Status != "started" {
		t.Fatalf("status = %q", r.Status)
	}
	evs := c.collectUntil("turn_end")
	echoed := false
	for _, e := range evs {
		if e["type"] == "received_prompt" && fmt.Sprint(e["text"]) == "Skill:greet to Ana" {
			echoed = true
		}
	}
	if !echoed {
		t.Fatalf("skill echo not found in %v", evs)
	}
	var m SessionMessagesResult
	c.must("session/messages", SessionMessagesParams{ID: d.ID}, &m)
	if raw := fmt.Sprint(m.Messages[0]); !strings.Contains(raw, "SAY-HELLO-BODY") || !strings.Contains(raw, "to Ana") {
		t.Fatalf("model did not get the skill: %s", raw)
	}

	c.fail("session/exec_command", SessionExecCommandParams{ID: d.ID, Command: "skill:missing"}, CodeNotFound, "not_found")
	je := c.fail("session/exec_command", SessionExecCommandParams{ID: d.ID, Command: "compact"}, CodeInvalid, "invalid")
	if !strings.Contains(je.Message, "session/compact") {
		t.Fatalf("message does not name the method: %s", je.Message)
	}
	c.fail("session/exec_command", SessionExecCommandParams{ID: d.ID, Command: "bogus"}, CodeInvalid, "invalid")
}

func TestSessionOpsUnknownSessionIsNotFound(t *testing.T) {
	c := start(t)
	c.initialize(memStoreConfig())
	ops := map[string]any{
		"session/prompt":             SessionPromptParams{ID: "nope", Text: "x"},
		"session/ask":                SessionPromptParams{ID: "nope", Text: "x"},
		"session/stop":               SessionIDParams{ID: "nope"},
		"session/compact":            SessionIDParams{ID: "nope"},
		"session/reset":              SessionIDParams{ID: "nope"},
		"session/switch_model":       SessionSwitchModelParams{ID: "nope", Model: "a/b"},
		"session/switch_thinking":    SessionSwitchThinkingParams{ID: "nope", Level: "low"},
		"session/set_max_iterations": SessionSetMaxIterationsParams{ID: "nope", N: 3},
		"session/rename":             SessionRenameParams{ID: "nope", Name: "x"},
		"session/info":               SessionIDParams{ID: "nope"},
		"session/context":            SessionIDParams{ID: "nope"},
		"session/sysprompt":          SessionIDParams{ID: "nope"},
		"session/tools":              SessionIDParams{ID: "nope"},
		"session/messages":           SessionMessagesParams{ID: "nope"},
		"session/exec_command":       SessionExecCommandParams{ID: "nope", Command: "skill:x"},
	}
	if len(ops) != len(sessionMoreOps()) {
		t.Fatalf("table covers %d ops, sessionMoreOps has %d", len(ops), len(sessionMoreOps()))
	}
	for m, p := range ops {
		c.fail(m, p, CodeNotFound, "not_found")
	}
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
