package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/internal/version"
)

const whoTool = `{"name":"Who","description":"d","input_schema":{"type":"object"}}`

func decl(name string) ToolDecl {
	return ToolDecl{Name: name, Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

// ── lifecycle ────────────────────────────────────────────────────────────

func TestInitializeOK(t *testing.T) {
	c := start(t)
	res := c.initialize(memStoreConfig())
	if res.ProtocolVersion != ProtocolVersion || res.HarnessVersion != version.Version {
		t.Fatalf("result = %+v", res)
	}
	if res.Capabilities.Channels == nil || len(res.Capabilities.Channels) != 0 {
		t.Fatalf("channels = %#v, want an empty array", res.Capabilities.Channels)
	}
}

func TestInitializeWrongVersionRepliesThenExits(t *testing.T) {
	c := start(t)
	je := c.fail("initialize", map[string]any{"protocol_version": 2}, CodeProtocolVersion, "protocol_version")
	d := je.Data.(map[string]any)
	if sup, _ := d["supported"].([]any); len(sup) != 1 || sup[0].(float64) != 1 {
		t.Fatalf("data = %v", d)
	}
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		c.done <- nil // for the cleanup
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not exit after a protocol version error")
	}
}

func TestInitializeMissingVersionIsAVersionError(t *testing.T) {
	c := start(t)
	c.fail("initialize", map[string]any{}, CodeProtocolVersion, "protocol_version")
}

func TestInitializeTwice(t *testing.T) {
	c := start(t)
	c.initialize(memStoreConfig())
	c.fail("initialize", InitializeParams{ProtocolVersion: ProtocolVersion}, CodeConflict, "conflict")
}

func TestMethodBeforeInitialize(t *testing.T) {
	c := start(t)
	c.fail("agent/profiles", nil, jsonrpc.CodeInvalidRequest, "")
	c.fail("session/new", map[string]any{"cwd": "/", "model": "x/y"}, jsonrpc.CodeInvalidRequest, "")
	// ...and initialize still works afterwards.
	c.initialize(memStoreConfig())
}

func TestUnknownMethodAndUnknownField(t *testing.T) {
	c := start(t)
	c.initialize(memStoreConfig())
	c.fail("nope/nothing", nil, jsonrpc.CodeMethodNotFound, "")
	je := c.fail("agent/register_tool", map[string]any{"tool": decl("A"), "extra": 1}, jsonrpc.CodeInvalidParams, "")
	if want := "extra"; !contains(je.Message, want) {
		t.Fatalf("message %q does not name the field", je.Message)
	}
	c.fail("session/close", map[string]any{"id": 5}, jsonrpc.CodeInvalidParams, "")
}

func TestInitializeUnknownConfigFieldIsInvalidParams(t *testing.T) {
	c := start(t)
	c.fail("initialize", map[string]any{"protocol_version": 1, "config": map[string]any{"thinking": "high"}}, jsonrpc.CodeInvalidParams, "")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ── config ───────────────────────────────────────────────────────────────

func TestConfigDefaults(t *testing.T) {
	c := start(t)
	c.initialize(Config{}) // file store under the (temp) HOME, no memory, no resources
	opts := c.srv.Agent().Options()
	if opts.Memory != nil {
		t.Fatal("memory must default to none")
	}
	if opts.ResourceLoader != nil {
		t.Fatal("resources default to NOTHING (nil), exactly as agent.New does")
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".harness", "agent", "sessions")); err != nil {
		t.Fatalf("the default file store was not opened: %v", err)
	}
}

// resources: {"kind":"file"} turns the discovery of the project's AGENTS.md,
// SYSTEM.md and skills on; any other kind is an error naming the field.
func TestConfigResourcesKinds(t *testing.T) {
	c := start(t)
	c.initialize(Config{Store: &KindConfig{Kind: "memory"}, Resources: &ResourcesConfig{Kind: "file"}})
	if c.srv.Agent().Options().ResourceLoader == nil {
		t.Fatal("resources kind file must install the file loader")
	}

	c2 := start(t)
	_, e := c2.call("initialize", InitializeParams{ProtocolVersion: ProtocolVersion, Config: Config{Resources: &ResourcesConfig{Kind: "none"}}})
	if e == nil || e.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("resources kind none: %v, want invalid params (a field is omitted to turn something off)", e)
	}
}

func TestConfigSqliteMemoryAndMemoryStore(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "mem.db")
	c := start(t)
	c.initialize(Config{Store: &KindConfig{Kind: "memory"}, Memory: &KindConfig{Kind: "sqlite", Path: db}})
	if c.srv.Agent().Options().Memory == nil {
		t.Fatal("sqlite memory was not opened")
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("database file: %v", err)
	}
	// store: memory → nothing under HOME/.harness/agent/sessions
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".harness", "agent", "sessions")); err == nil {
		t.Fatal("store kind memory must not create the sessions directory")
	}
}

func TestConfigFileStoreWithPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sess")
	c := start(t)
	c.initialize(Config{Store: &KindConfig{Kind: "file", Path: dir}})
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("store path not created: %v", err)
	}
}

func TestConfigInvalidKindsNameFieldAndAllowed(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want []string
	}{
		"store":     {Config{Store: &KindConfig{Kind: "s3"}}, []string{"config.store.kind", "file", "memory"}},
		"memory":    {Config{Memory: &KindConfig{Kind: "redis"}}, []string{"config.memory.kind", "sqlite", "none"}},
		"resources": {Config{Resources: &ResourcesConfig{Kind: "web"}}, []string{"config.resources.kind", "file"}},
		"path":      {Config{Store: &KindConfig{Kind: "memory", Path: "/x"}}, []string{"config.store.path"}},
		"thinking":  {Config{ThinkingLevel: "ultra"}, []string{"thinking_level"}},
		"channel":   {Config{Channels: []string{"irc"}}, []string{"irc"}},
	} {
		t.Run(name, func(t *testing.T) {
			c := start(t)
			je := c.fail("initialize", InitializeParams{ProtocolVersion: 1, Config: tc.cfg}, jsonrpc.CodeInvalidParams, "")
			for _, w := range tc.want {
				if !contains(je.Message, w) {
					t.Errorf("message %q lacks %q", je.Message, w)
				}
			}
			// a failed initialize can be retried
			c.initialize(memStoreConfig())
		})
	}
}

func TestConfigChannelWithoutCredentialsIsInvalid(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	c := start(t) // fresh HOME: no Slack login, no Telegram token
	for _, ch := range []string{"slack", "telegram"} {
		je := c.fail("initialize", InitializeParams{ProtocolVersion: 1, Config: Config{Channels: []string{ch}}}, CodeInvalid, "invalid")
		if !contains(je.Message, ch) {
			t.Errorf("message %q does not name %s", je.Message, ch)
		}
	}
	// colleagues needs no credentials
	res := c.initialize(Config{Store: &KindConfig{Kind: "memory"}, Channels: []string{"colleagues"}})
	if len(res.Capabilities.Channels) != 1 || res.Capabilities.Channels[0] != "colleagues" {
		t.Fatalf("channels = %v", res.Capabilities.Channels)
	}
}

// ── sessions and events ──────────────────────────────────────────────────

func TestSessionNewAndEventsAsNotifications(t *testing.T) {
	model := registerModel(t, &fakeModel{})
	c := start(t)
	c.initialize(memStoreConfig())

	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model, Name: "named"}, &d)
	if d.ID == "" || d.Name != "named" || d.Model != model || d.MaxIterations == 0 {
		t.Fatalf("detail = %+v", d)
	}
	var ids IDsResult
	c.must("agent/active_sessions", nil, &ids)
	if len(ids.IDs) != 1 || ids.IDs[0] != d.ID {
		t.Fatalf("active = %v", ids.IDs)
	}

	sess, err := c.srv.session(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := sess.PromptAndWait(context.Background(), "hi"); err != nil || out != "ok" {
		t.Fatalf("turn: %q %v", out, err)
	}
	evs := c.collectUntil("turn_end")
	var types []string
	var text string
	for _, e := range evs {
		if e["session_id"] != d.ID {
			t.Fatalf("event %v lacks the session id", e)
		}
		types = append(types, e["type"].(string))
		if e["type"] == "text" {
			text += e["delta"].(string)
		}
		if e["type"] == "tokens" {
			for _, k := range []string{"input", "context_usage", "cost_usd"} {
				if _, ok := e[k]; !ok {
					t.Errorf("tokens event lacks %q: %v", k, e)
				}
			}
		}
	}
	if text != "ok" || types[0] != "received_prompt" || types[1] != "turn_start" || types[len(types)-1] != "turn_end" {
		t.Fatalf("events = %v text=%q", types, text)
	}
}

func TestSessionNewValidationAndErrors(t *testing.T) {
	c := start(t)
	c.initialize(memStoreConfig())
	c.fail("session/new", SessionNewParams{CWD: "/"}, CodeInvalid, "invalid")
	c.fail("session/new", SessionNewParams{CWD: "/", Model: "nope/none"}, CodeNotFound, "provider_not_found")
}

func TestSessionCloseStopsEventsAndForgetsIt(t *testing.T) {
	model := registerModel(t, &fakeModel{})
	c := start(t)
	c.initialize(memStoreConfig())
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
	c.must("session/close", SessionIDParams{ID: d.ID}, nil)
	c.fail("session/close", SessionIDParams{ID: d.ID}, CodeNotFound, "not_found")
	var ids IDsResult
	c.must("agent/active_sessions", nil, &ids)
	if len(ids.IDs) != 0 {
		t.Fatalf("active after close = %v", ids.IDs)
	}
}

func TestSessionResumeForkImport(t *testing.T) {
	model := registerModel(t, &fakeModel{})
	dir := t.TempDir()
	c := start(t)
	c.initialize(Config{Store: &KindConfig{Kind: "file", Path: dir}})
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model, Name: "orig"}, &d)
	sess, _ := c.srv.session(d.ID)
	if _, err := sess.PromptAndWait(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	// resume of the open session returns it (no second pump)
	var r agent.SessionDetail
	c.must("session/resume", SessionResumeParams{ID: d.ID}, &r)
	if r.ID != d.ID {
		t.Fatalf("resume = %+v", r)
	}

	// fork
	var f agent.SessionDetail
	c.must("session/fork", SessionForkParams{ID: d.ID}, &f)
	if f.ID == d.ID || f.Model != model {
		t.Fatalf("fork = %+v", f)
	}

	// close + resume from disk
	c.must("session/close", SessionIDParams{ID: d.ID}, nil)
	c.must("session/resume", SessionResumeParams{ID: d.ID}, &r)
	if r.Name != "orig" {
		t.Fatalf("resumed = %+v", r)
	}
	c.fail("session/resume", SessionResumeParams{ID: "does-not-exist"}, CodeNotFound, "not_found")
	c.fail("session/fork", SessionForkParams{ID: "does-not-exist"}, CodeNotFound, "not_found")

	// export through the agent's admin, import by content and by path
	var buf bytesBuffer
	if _, err := c.srv.Admin().Sessions.Export(d.ID, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	var imp agent.SessionDetail
	c.must("session/import", SessionImportParams{Content: buf.String()}, &imp)
	if imp.ID == d.ID || imp.Name != "orig" {
		t.Fatalf("import = %+v", imp)
	}
	file := filepath.Join(t.TempDir(), "x.export.jsonl")
	os.WriteFile(file, buf.Bytes(), 0o600)
	var imp2 agent.SessionDetail
	c.must("session/import", SessionImportParams{Path: file}, &imp2)
	if imp2.ID == imp.ID {
		t.Fatal("import must create a new session each time")
	}
	c.fail("session/import", SessionImportParams{}, CodeInvalid, "invalid")
	c.fail("session/import", SessionImportParams{Path: file, Content: "x"}, CodeInvalid, "invalid")
	c.fail("session/import", SessionImportParams{Path: "/etc/passwd"}, CodeInvalid, "invalid")
	c.fail("session/import", SessionImportParams{Path: filepath.Join(dir, "none.export.jsonl")}, CodeNotFound, "not_found")
	c.fail("session/import", SessionImportParams{Content: "not an export\n"}, CodeInvalid, "invalid_export")
}

// ── profiles ─────────────────────────────────────────────────────────────

func TestProfilesRoundTrip(t *testing.T) {
	model := registerModel(t, &fakeModel{})
	c := start(t)
	c.initialize(memStoreConfig())
	c.must("agent/register_profile", RegisterProfileParams{Name: "p1", Profile: ProfileConfig{Directives: []string{"## P1"}, Tools: []ToolDecl{decl("P1Tool")}}}, nil)
	var names NamesResult
	c.must("agent/profiles", nil, &names)
	if len(names.Names) != 1 || names.Names[0] != "p1" {
		t.Fatalf("profiles = %v", names.Names)
	}
	c.fail("agent/register_profile", RegisterProfileParams{Name: "  "}, CodeInvalid, "invalid")

	// unknown profile fails the open
	c.fail("session/new", SessionNewParams{CWD: t.TempDir(), Model: model, SessionOptions: SessionOptions{EnabledProfiles: &[]string{"zz"}}}, CodeInvalid, "invalid")

	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model, SessionOptions: SessionOptions{EnabledProfiles: &[]string{"p1"}}}, &d)
	if len(d.Profiles) != 1 || d.Profiles[0] != "p1" {
		t.Fatalf("profiles = %v", d.Profiles)
	}
	sess, _ := c.srv.session(d.ID)
	if !hasTool(sess, "P1Tool") {
		t.Fatal("the profile's tool is not in the session")
	}
	// absent = unchanged; [] = remove all
	id0 := d.ID
	d = agent.SessionDetail{}
	c.must("session/resume", SessionResumeParams{ID: id0}, &d)
	if len(d.Profiles) != 1 {
		t.Fatalf("absent enabled_profiles changed the binding: %v", d.Profiles)
	}
	id := d.ID
	d = agent.SessionDetail{}
	c.must("session/resume", json.RawMessage(fmt.Sprintf(`{"id":%q,"enabled_profiles":[]}`, id)), &d)
	if len(d.Profiles) != 0 {
		t.Fatalf("[] must remove all: %v", d.Profiles)
	}

	c.must("agent/unregister_profile", ProfileNameParams{Name: "p1"}, nil)
	c.must("agent/profiles", nil, &names)
	if len(names.Names) != 0 {
		t.Fatalf("profiles after unregister = %v", names.Names)
	}
}

func hasTool(s *agent.Session, name string) bool {
	for _, d := range s.Tools() {
		if d.Name == name {
			return true
		}
	}
	return false
}

// ── tool callbacks ───────────────────────────────────────────────────────

func newToolSession(t *testing.T, calls []string, decls ...ToolDecl) (*client, *agent.Session, string) {
	t.Helper()
	model := registerModel(t, &fakeModel{calls: calls})
	c := start(t)
	c.initialize(memStoreConfig(decls...))
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
	sess, err := c.srv.session(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, sess, d.ID
}

func TestDeclaredToolIsCalledBack(t *testing.T) {
	c, sess, id := newToolSession(t, []string{"Who"}, decl("Who"))
	got := make(chan ToolCallParams, 1)
	c.setTool(func(_ context.Context, p ToolCallParams) (ToolCallResult, error) {
		got <- p
		return ToolCallResult{Content: "alice"}, nil
	})
	out, err := sess.PromptAndWait(context.Background(), "who?")
	if err != nil || out != "got:alice" {
		t.Fatalf("turn = %q, %v", out, err)
	}
	p := <-got
	if p.SessionID != id || p.Name != "Who" || p.ToolCallID == "" || string(p.Input) != `{"n":0}` {
		t.Fatalf("tool/call params = %+v", p)
	}
}

func TestToolIsErrorReachesTheModelAsAnError(t *testing.T) {
	c, sess, _ := newToolSession(t, []string{"Who"}, decl("Who"))
	c.setTool(func(context.Context, ToolCallParams) (ToolCallResult, error) {
		return ToolCallResult{Content: "it broke", IsError: true}, nil
	})
	out, err := sess.PromptAndWait(context.Background(), "who?")
	if err != nil || !contains(out, "it broke") {
		t.Fatalf("turn = %q, %v", out, err)
	}
	// the turn saw tool_result with is_error true
	for _, e := range c.collectUntil("turn_end") {
		if e["type"] == "tool_result" && e["is_error"] != true {
			t.Fatalf("tool_result = %v", e)
		}
	}
}

func TestToolClientErrorResponseFailsTheTool(t *testing.T) {
	c, sess, _ := newToolSession(t, []string{"Who"}, decl("Who"))
	c.setTool(func(context.Context, ToolCallParams) (ToolCallResult, error) {
		return ToolCallResult{}, &jsonrpc.Error{Code: -32000, Message: "client exploded"}
	})
	out, err := sess.PromptAndWait(context.Background(), "who?")
	if err != nil || !contains(out, "client exploded") {
		t.Fatalf("turn = %q, %v", out, err)
	}
}

func TestToolImagesReachTheBridge(t *testing.T) {
	c, _, _ := newToolSession(t, nil, decl("Who"))
	c.setTool(func(context.Context, ToolCallParams) (ToolCallResult, error) {
		return ToolCallResult{Content: "see", Images: []imageData{{MimeType: "image/png", Base64: "AAAA"}}}, nil
	})
	out, imgs, err := c.srv.callTool(context.Background(), "Who", json.RawMessage(`{}`))
	if err != nil || out != "see" || len(imgs) != 1 || imgs[0].Base64 != "AAAA" {
		t.Fatalf("callTool = %q %v %v", out, imgs, err)
	}
}

func TestStopSendsToolCancel(t *testing.T) {
	c, sess, _ := newToolSession(t, []string{"Who"}, decl("Who"))
	var callID string
	started := make(chan struct{})
	c.setTool(func(ctx context.Context, p ToolCallParams) (ToolCallResult, error) {
		callID = p.ToolCallID
		close(started)
		<-ctx.Done() // the client's own handler is cancelled only at shutdown; wait to be answered late
		return ToolCallResult{Content: "late"}, nil
	})
	turn := make(chan error, 1)
	go func() { _, err := sess.PromptAndWait(context.Background(), "who?"); turn <- err }()
	<-started
	sess.Stop()
	select {
	case p := <-c.cancel:
		if p.ToolCallID != callID {
			t.Fatalf("tool/cancel for %q, want %q", p.ToolCallID, callID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tool/cancel after session stop")
	}
	<-turn
	// the session is usable afterwards (a late answer is dropped silently)
	c.setTool(func(context.Context, ToolCallParams) (ToolCallResult, error) {
		return ToolCallResult{Content: "again"}, nil
	})
	if out, err := sess.PromptAndWait(context.Background(), "who?"); err != nil || out != "got:again" {
		t.Fatalf("next turn = %q, %v", out, err)
	}
}

func TestClientDisconnectMidCallFailsToolAndShutsDown(t *testing.T) {
	c, sess, _ := newToolSession(t, []string{"Who"}, decl("Who"))
	started := make(chan struct{})
	c.setTool(func(ctx context.Context, p ToolCallParams) (ToolCallResult, error) {
		close(started)
		<-ctx.Done()
		return ToolCallResult{}, ctx.Err()
	})
	turn := make(chan error, 1)
	go func() { _, err := sess.PromptAndWait(context.Background(), "who?"); turn <- err }()
	<-started
	c.inW.Close() // the client dies
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		c.done <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the client disconnected mid-call")
	}
	select {
	case <-turn:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn is stuck on the dead client")
	}
}

// With no ctx to cancel, ONLY the connection closing can end a pending
// tool/call: the bridge must fail it (not hang) so the turn can go on.
func TestPendingToolCallFailsWhenTheConnectionCloses(t *testing.T) {
	c, _, _ := newToolSession(t, nil, decl("Who"))
	started := make(chan struct{})
	c.setTool(func(ctx context.Context, p ToolCallParams) (ToolCallResult, error) {
		close(started)
		<-ctx.Done()
		return ToolCallResult{}, ctx.Err()
	})
	res := make(chan error, 1)
	go func() {
		_, _, err := c.srv.callTool(context.Background(), "Who", json.RawMessage(`{}`))
		res <- err
	}()
	<-started
	c.inW.Close()
	select {
	case err := <-res:
		if err == nil || !contains(err.Error(), "disconnected") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a pending tool call survived the connection closing")
	}
}

func TestTwoConcurrentToolCallsAnsweredOutOfOrder(t *testing.T) {
	c, sess, _ := newToolSession(t, []string{"Who", "Who"}, decl("Who"))
	arrived := make(chan ToolCallParams, 2)
	release := map[string]chan struct{}{}
	var mu sync.Mutex
	c.setTool(func(_ context.Context, p ToolCallParams) (ToolCallResult, error) {
		mu.Lock()
		ch := make(chan struct{})
		release[string(p.Input)] = ch
		mu.Unlock()
		arrived <- p
		<-ch
		return ToolCallResult{Content: "r" + string(p.Input)}, nil
	})
	turn := make(chan string, 1)
	go func() { out, _ := sess.PromptAndWait(context.Background(), "go"); turn <- out }()
	a, b := <-arrived, <-arrived // both are in flight at the same time
	if a.ToolCallID == b.ToolCallID {
		t.Fatal("tool_call_id must be unique per call")
	}
	mu.Lock()
	first, second := release[`{"n":1}`], release[`{"n":0}`]
	mu.Unlock()
	close(first) // answer the SECOND call first
	time.Sleep(50 * time.Millisecond)
	close(second)
	select {
	case out := <-turn:
		// results are appended as the calls complete: the second one finished first
		if out != `got:r{"n":1}|r{"n":0}` {
			t.Fatalf("turn = %q", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
}

func TestRegisterToolAfterInitialize(t *testing.T) {
	model := registerModel(t, &fakeModel{calls: []string{"Late"}})
	c := start(t)
	c.initialize(memStoreConfig())
	c.must("agent/register_tool", RegisterToolParams{Tool: decl("Late")}, nil)
	c.fail("agent/register_tool", RegisterToolParams{Tool: ToolDecl{}}, CodeInvalid, "invalid")
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
	sess, _ := c.srv.session(d.ID)
	c.setTool(func(_ context.Context, p ToolCallParams) (ToolCallResult, error) {
		return ToolCallResult{Content: p.Name}, nil
	})
	if out, err := sess.PromptAndWait(context.Background(), "x"); err != nil || out != "got:Late" {
		t.Fatalf("turn = %q %v", out, err)
	}
}

// ── shutdown ─────────────────────────────────────────────────────────────

func TestStdinEOFShutsEverythingDown(t *testing.T) {
	model := registerModel(t, &fakeModel{})
	c := start(t)
	c.initialize(memStoreConfig())
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
	a := c.srv.Agent()
	sess, _ := c.srv.session(d.ID)

	c.inW.Close()
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on EOF", err)
		}
		c.done <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if ids := a.ActiveSessions(); len(ids) != 0 {
		t.Fatalf("sessions still open: %v", ids)
	}
	if _, ok := <-sess.Watch(1, nil).Events(); ok {
		t.Fatal("the session was not closed")
	}
	if err := a.Close(); err != nil { // idempotent: already closed by the shutdown
		t.Fatal(err)
	}
}

func TestContextCancelShutsDownAndReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := pipe()
	defer inW.Close()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, WithStdin(inR), WithStdout(discard{})) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return on cancel")
	}
}

func TestAgentShutdownRepliesThenExits(t *testing.T) {
	model := registerModel(t, &fakeModel{})
	c := start(t)
	c.initialize(memStoreConfig())
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
	a := c.srv.Agent()
	c.must("agent/shutdown", nil, nil) // the reply arrives
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
		c.done <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("agent/shutdown did not end Run")
	}
	if len(a.ActiveSessions()) != 0 {
		t.Fatal("sessions open after shutdown")
	}
}

func TestMalformedLineDoesNotKillTheServer(t *testing.T) {
	c := start(t)
	if _, err := c.inW.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	c.initialize(memStoreConfig())
}

// ── error mapping ────────────────────────────────────────────────────────

func TestRPCErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
		kind string
	}{
		"unknown profile":   {fmt.Errorf("x: %w", agent.ErrUnknownProfile), CodeInvalid, "invalid"},
		"channel invalid":   {agent.ErrChannelInvalid, CodeInvalid, "invalid"},
		"channel absent":    {agent.ErrChannelUnavailable, CodeInvalid, "invalid"},
		"channel taken":     {agent.ErrChannelTaken, CodeConflict, "conflict"},
		"busy":              {agent.ErrBusy, CodeConflict, "conflict"},
		"no model":          {agent.ErrNoModelAvailable, CodeNotConnected, "not_connected"},
		"session not found": {fmt.Errorf("resume session: session x not found"), CodeNotFound, "not_found"},
		"other":             {fmt.Errorf("boom"), jsonrpc.CodeInternalError, "internal"},
	} {
		je := rpcError(tc.err)
		d, _ := je.Data.(map[string]any)
		if je.Code != tc.code || d["kind"] != tc.kind {
			t.Errorf("%s: got %d %v, want %d %s", name, je.Code, je.Data, tc.code, tc.kind)
		}
	}
	if rpcError(nil) != nil {
		t.Error("nil must stay nil")
	}
}

// A client may send `initialize` and, without waiting, more requests: they wait
// for the agent to be built instead of failing "not initialized" (requests run
// concurrently, so they can reach dispatch before `initialize` does — Received
// marks it as begun in arrival order). One sent when no `initialize` was sent
// still fails at once. Repeated: the race only shows sometimes.
func TestRequestsPipelinedBehindInitializeNeverFail(t *testing.T) {
	for run := 0; run < 40; run++ {
		c := start(t)
		type out struct {
			method string
			err    *jsonrpc.Error
		}
		results := make(chan out, 6)
		call := func(method string, params any) {
			_, e := c.call(method, params)
			results <- out{method, e}
		}
		// Written in this order, back to back, answered concurrently.
		c.conn.Notify("initialize", nil) // not a request: must be ignored, never start anything
		go call("initialize", InitializeParams{ProtocolVersion: ProtocolVersion, Config: memStoreConfig()})
		for _, m := range []string{"agent/profiles", "agent/active_sessions", "admin/settings/get"} {
			go call(m, nil)
		}
		for i := 0; i < 4; i++ {
			select {
			case o := <-results:
				if o.err != nil {
					t.Fatalf("run %d: %s failed: %v", run, o.method, o.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("run %d: a request never answered", run)
			}
		}
	}
}

// Without any `initialize` a request fails at once, it does not wait forever.
func TestRequestBeforeAnyInitializeFailsAtOnce(t *testing.T) {
	c := start(t)
	if _, e := c.call("agent/profiles", nil); e == nil || e.Code != jsonrpc.CodeInvalidRequest {
		t.Fatalf("got %v, want an invalid-request (not initialized) error", e)
	}
}

// While `initialize` is building the agent, a request waits for it (awaitReady).
func TestAwaitReadyWaitsWhileInitializing(t *testing.T) {
	s := &Server{state: stateInitializing}
	got := make(chan bool, 1)
	go func() { got <- s.awaitReady(context.Background()) }()
	select {
	case <-got:
		t.Fatal("returned while still initializing")
	case <-time.After(60 * time.Millisecond):
	}
	s.mu.Lock()
	s.state = stateReady
	s.mu.Unlock()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("not ready after initialize finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never woke up")
	}
	// A cancelled ctx gives up; a connection that never started initializing fails at once.
	s2 := &Server{state: stateInitializing}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s2.awaitReady(ctx) {
		t.Fatal("ready with a cancelled context")
	}
	if (&Server{state: stateNew}).awaitReady(context.Background()) {
		t.Fatal("a never-initialized connection must not be ready")
	}
}
