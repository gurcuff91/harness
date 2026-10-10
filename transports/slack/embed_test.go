package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	agentstore "github.com/gurcuff91/harness/agent/store"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

// newSharedServer is ONE running server over a fresh agent with a fake
// model — the SDK consumer's single server every frontend shares.
func newSharedServer(t *testing.T, opts agent.AgentOptions) (*server.Server, *agent.Agent, string) {
	t.Helper()
	isolateConfig(t)
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(models.Close)
	provName := "slack-embed-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if err := agent.NewOpenAIProvider(provName, models.URL); err != nil {
		t.Fatal(err)
	}
	opts.Store = agentstore.NewInMemoryStore()
	a := agent.New(opts)
	t.Cleanup(func() { a.Close() })
	srv, err := server.Start(a, "", server.ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, a, provName + "/m"
}

func embedTransport(t *testing.T, opts Options) (*Transport, *agent.Agent) {
	t.Helper()
	srv, a, model := newSharedServer(t, agent.AgentOptions{})
	return startEmbedded(t, srv, opts, model), a
}

// startEmbedded wires one Slack Transport onto the shared srv exactly like
// runWithOptions does after AuthTest — registering the "slack" profile
// (Directive + tools bound to THIS transport's bot) — with the bot pointed
// at a fake Slack API.
func startEmbedded(t *testing.T, srv *server.Server, opts Options, model string) *Transport {
	t.Helper()
	fake := newFakeSlackServer(t)
	t.Cleanup(fake.Close)
	st, _ := openStore(opts.CWD)
	tr := &Transport{
		opts:        opts,
		api:         client.New(srv.Addr()),
		bot:         NewBot(fake.URL, "xoxc", "xoxd"),
		store:       st,
		srv:         srv,
		logger:      logx.NewNilLogger(),
		model:       model,
		cwd:         st.cwd,
		myID:        "UME",
		pumps:       map[string]*channelPump{},
		pendingAsks: map[string]chan askReply{},
	}
	slackTools := SlackTools(tr.bot, tr.myID, tr)
	srv.RegisterProfile(ProfileName, server.Profile{
		Directives: []string{Directive},
		Tools:      func() []atools.Tool { return slackTools },
	})
	return tr
}

// Criterion 6: same per-session directive + WithCWD behavior as Telegram —
// created and resumed sessions carry slack.Directive, others don't.
func TestEmbeddedSlackCWDAndSessionDirective(t *testing.T) {
	tr, a := embedTransport(t, Options{CWD: "/x"})

	id, err := tr.acquireSession("C123")
	if err != nil {
		t.Fatalf("acquireSession: %v", err)
	}
	sess, _ := a.ResumeSession(id)
	if sess.CWD() != "/x" {
		t.Errorf("session cwd = %q, want /x", sess.CWD())
	}
	if got, _ := ChannelSessions("/x"); got["C123"] != id {
		t.Errorf("ChannelSessions(/x) = %v, want C123 → %s", got, id)
	}
	if !strings.Contains(sess.SystemPrompt(), Directive) {
		t.Error("the Slack session's prompt must include slack.Directive")
	}

	other, err := tr.api.CreateSession(tr.model, "/elsewhere", "")
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := a.ResumeSession(other.ID); strings.Contains(o.SystemPrompt(), Directive) {
		t.Error("a session created outside the transport must not include slack.Directive")
	}

	// Resume after the live session is gone (restart): directive re-added.
	sess.Close()
	tr.api.CloseSession(id)
	again, err := tr.acquireSession("C123")
	if err != nil || again != id {
		t.Fatalf("re-acquire = %q, %v; want %q", again, err, id)
	}
	if s, _ := a.ResumeSession(id); !strings.Contains(s.SystemPrompt(), Directive) {
		t.Error("a resumed Slack session must include slack.Directive")
	}
}

var slackToolNames = []string{"SlackListChannels", "SlackListUsers", "SlackPost", "SlackMessages", "SlackAsk"}

func toolNames(t *testing.T, s *agent.Session) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for _, d := range s.Tools() {
		got[d.Name] = true
	}
	return got
}

func hasAnySlackTool(names map[string]bool) bool {
	for _, n := range slackToolNames {
		if names[n] {
			return true
		}
	}
	return false
}

// Criteria 1+2: Slack sessions have all 5 Slack tools (+ the directive);
// a session created on the same agent by any other path has none — and the
// agent-wide registry holds none.
func TestSlackToolsAreScopedToSlackSessions(t *testing.T) {
	tr, a := embedTransport(t, Options{CWD: "/x"})

	id, err := tr.acquireSession("C1")
	if err != nil {
		t.Fatal(err)
	}
	slackSess, _ := a.ResumeSession(id)
	names := toolNames(t, slackSess)
	for _, n := range slackToolNames {
		if !names[n] {
			t.Errorf("Slack session is missing %s", n)
		}
	}
	if !strings.Contains(slackSess.SystemPrompt(), Directive) {
		t.Error("Slack session is missing slack.Directive")
	}

	// Another path: the SDK consumer's own direct NewSession on the same agent.
	other, err := a.NewSession("/cards", tr.model)
	if err != nil {
		t.Fatal(err)
	}
	if hasAnySlackTool(toolNames(t, other)) {
		t.Errorf("a non-Slack session leaked Slack tools: %v", toolNames(t, other))
	}
	for _, d := range a.MCPTools() {
		if strings.HasPrefix(d.Def.Name, "Slack") {
			t.Errorf("agent-wide tool %s", d.Def.Name)
		}
	}
}

// Stopping Slack (its shutdown) unregisters the profile and closes its
// sessions; the shared server and agent stay up and clean. A second run binds
// its sessions to the NEW transport's tools.
func TestSlackStopKeepsServerAndRestartRebinds(t *testing.T) {
	srv, a, model := newSharedServer(t, agent.AgentOptions{})
	tr1 := startEmbedded(t, srv, Options{CWD: "/x"}, model)
	id1, err := tr1.acquireSession("C1")
	if err != nil {
		t.Fatal(err)
	}
	tr1.pumps["C1"] = &channelPump{sessionID: id1} // what pumpFor records
	s1, _ := a.ResumeSession(id1)

	tr1.shutdown() // what Run's deferred teardown does on ctx cancel

	if slices.Contains(srv.ProfileNames(), ProfileName) {
		t.Error("the slack profile must be unregistered on stop")
	}
	after, err := a.NewSession("/cards", model)
	if err != nil {
		t.Fatalf("agent unusable after Slack stopped: %v", err)
	}
	if hasAnySlackTool(toolNames(t, after)) {
		t.Error("a session created after Slack stopped has Slack tools")
	}

	tr2 := startEmbedded(t, srv, Options{CWD: "/x"}, model)
	id, err := tr2.acquireSession("C1")
	if err != nil || id != id1 {
		t.Fatalf("re-run acquire = %q, %v; want stored %q", id, err, id1)
	}
	s2, _ := a.ResumeSession(id)
	if s2 == s1 {
		t.Fatal("re-run got the stale live session closed on stop")
	}
	if !toolNames(t, s2)["SlackPost"] {
		t.Fatal("re-run Slack session lacks SlackPost")
	}
}

// DisallowedTools still applies to profile tools.
func TestSlackSessionToolsRespectDisallowedTools(t *testing.T) {
	srv, a, model := newSharedServer(t, agent.AgentOptions{DisallowedTools: []string{"SlackPost"}})
	tr := startEmbedded(t, srv, Options{CWD: "/x"}, model)
	id, err := tr.acquireSession("C1")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := a.ResumeSession(id)
	names := toolNames(t, s)
	if names["SlackPost"] {
		t.Error("DisallowedTools must filter profile SlackPost")
	}
	if !names["SlackListChannels"] {
		t.Error("the other Slack tools must remain")
	}
}

// The race the story is about: a plain client (the consumer's UI) opens a
// Slack session FIRST, with no options — it still gets the Slack directive
// and tools, from the persisted binding.
func TestSlackSessionOpenedFirstByPlainClientGetsProfile(t *testing.T) {
	srv, a, model := newSharedServer(t, agent.AgentOptions{})
	tr := startEmbedded(t, srv, Options{CWD: "/x"}, model)
	id, err := tr.acquireSession("C1")
	if err != nil {
		t.Fatal(err)
	}
	// Restart: everything closed, profile re-registered by a new run.
	tr.pumps["C1"] = &channelPump{sessionID: id}
	tr.shutdown()
	startEmbedded(t, srv, Options{CWD: "/x"}, model)

	ui := client.New(srv.Addr())
	if _, err := ui.ResumeSession(id); err != nil { // no options at all
		t.Fatal(err)
	}
	s, _ := a.ResumeSession(id)
	if !strings.Contains(s.SystemPrompt(), Directive) || !toolNames(t, s)["SlackPost"] {
		t.Fatal("a Slack session opened first by a plain client must get the slack profile")
	}
	if got := a.SessionProfiles(id); !slices.Equal(got, []string{ProfileName}) {
		t.Errorf("persisted binding = %v, want [slack]", got)
	}
}

// End to end through the REAL slack.Run on a SHARED server: while it runs,
// non-Slack sessions never get Slack tools; after ctx is cancelled Run returns
// without closing the server or the agent, and leaves no Slack tools behind.
func TestRunOnSharedServer(t *testing.T) {
	srv, a, model := newSharedServer(t, agent.AgentOptions{})
	slackAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth.test"):
			w.Write([]byte(`{"ok":true,"user_id":"UME","team":"T"}`))
		default: // rtm.connect etc. fail → Run retries until ctx is cancelled
			w.Write([]byte(`{"ok":false,"error":"not_in_test"}`))
		}
	}))
	defer slackAPI.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, srv, WithWorkspace(slackAPI.URL), WithXoxC("xoxc"), WithXoxD("xoxd"),
			WithSessionModel(model), WithCWD("/x"))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(srv.ProfileNames(), ProfileName) {
		if time.Now().After(deadline) {
			t.Fatal("slack profile never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mid, err := a.NewSession("/cards", model)
	if err != nil {
		t.Fatal(err)
	}
	if hasAnySlackTool(toolNames(t, mid)) {
		t.Error("while Slack runs, a non-Slack session got Slack tools")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if slices.Contains(srv.ProfileNames(), ProfileName) {
		t.Error("slack profile left registered after Run returned")
	}
	// Server still serving, agent still open.
	if _, err := client.New(srv.Addr()).CreateSession(model, "/cards", ""); err != nil {
		t.Fatalf("shared server/agent unusable after slack.Run: %v", err)
	}
}

// Run on a server that isn't serving fails clearly.
func TestRunRequiresServingServer(t *testing.T) {
	isolateConfig(t)
	a := agent.New(agent.AgentOptions{Store: agentstore.NewInMemoryStore()})
	defer a.Close()
	err := Run(context.Background(), server.NewServer(a, server.ServerOptions{}), WithWorkspace("x"), WithXoxC("x"), WithXoxD("x"))
	if err == nil || !strings.Contains(err.Error(), "not serving") {
		t.Fatalf("Run on an unstarted server = %v, want a clear not-serving error", err)
	}
}
