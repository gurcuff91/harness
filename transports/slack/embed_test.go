package slack

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
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

// embedTransport builds a Transport the way runWithOptions does — real
// in-process server + real SDK client over an agent with a fake model — but
// without a bot (acquireSession never touches it).
func embedTransport(t *testing.T, opts Options) (*Transport, *agent.Agent) {
	t.Helper()
	isolateConfig(t)
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(models.Close)
	provName := "slack-embed-" + strings.ToLower(t.Name())
	if err := agent.NewOpenAIProvider(provName, models.URL); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: agentstore.NewInMemoryStore()})
	t.Cleanup(func() { a.Close() })
	return startEmbedded(t, a, opts, provName+"/m"), a
}

// startEmbedded wires one Slack Transport onto a (possibly shared) agent
// exactly like runWithOptions does after AuthTest: its own in-process server
// carrying the Slack tools (SessionTools) and never closing the agent
// (KeepAgentOpen). The bot points at a fake Slack API.
func startEmbedded(t *testing.T, a *agent.Agent, opts Options, model string) *Transport {
	t.Helper()
	fake := newFakeSlackServer(t)
	t.Cleanup(fake.Close)
	st, _ := openStore(opts.CWD)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tr := &Transport{
		opts:        opts,
		agent:       a,
		api:         client.New(ln.Addr().String()),
		bot:         NewBot(fake.URL, "xoxc", "xoxd"),
		store:       st,
		logger:      logx.NewNilLogger(),
		model:       model,
		cwd:         st.cwd,
		myID:        "UME",
		pumps:       map[string]*channelPump{},
		pendingAsks: map[string]chan askReply{},
	}
	slackTools := SlackTools(tr.bot, tr.myID, tr)
	tr.srv = server.NewServer(a, server.ServerOptions{
		Logger:        logx.NewNilLogger(),
		Transport:     "slack",
		SessionTools:  func() []atools.Tool { return slackTools },
		KeepAgentOpen: true,
	})
	go tr.srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { tr.srv.Close() })
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

// Criteria 3+4: stopping Slack leaves the (caller-owned) agent alive with no
// Slack tools anywhere; a second Slack run on the same agent binds its
// sessions to the NEW transport's tools.
func TestSlackStopKeepsAgentAndRestartRebinds(t *testing.T) {
	tr1, a := embedTransport(t, Options{CWD: "/x"})
	id1, err := tr1.acquireSession("C1")
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := a.ResumeSession(id1) // live handle, holding tr1's tool closures

	tr1.srv.Close() // what Run's deferred Close does on ctx cancel

	// The agent must still be fully usable, and clean.
	after, err := a.NewSession("/cards", tr1.model)
	if err != nil {
		t.Fatalf("agent unusable after Slack stopped: %v", err)
	}
	if hasAnySlackTool(toolNames(t, after)) {
		t.Error("a session created after Slack stopped has Slack tools")
	}

	// Second run on the same agent: its sessions get ITS tools.
	tr2 := startEmbedded(t, a, Options{CWD: "/x"}, tr1.model)
	id, err := tr2.acquireSession("C1")
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := a.ResumeSession(id)
	if !toolNames(t, s2)["SlackPost"] {
		t.Fatal("re-run Slack session lacks SlackPost")
	}
	// The binding guarantee: stopping tr1 closed its sessions, so tr2's resume
	// REBUILT the session (with tr2's tool closures) instead of returning the
	// stale live object still bound to tr1's bot/transport.
	if id != id1 {
		t.Fatalf("re-run resumed %s, want the stored session %s", id, id1)
	}
	if s2 == s1 {
		t.Fatal("re-run got the stale live session bound to the stopped transport")
	}
}

// Criterion 5: DisallowedTools still applies to session-scoped tools.
func TestSlackSessionToolsRespectDisallowedTools(t *testing.T) {
	isolateConfig(t)
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	if err := agent.NewOpenAIProvider("slack-disallow-fake", models.URL); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: agentstore.NewInMemoryStore(), DisallowedTools: []string{"SlackPost"}})
	defer a.Close()
	tr := startEmbedded(t, a, Options{CWD: "/x"}, "slack-disallow-fake/m")
	id, err := tr.acquireSession("C1")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := a.ResumeSession(id)
	names := toolNames(t, s)
	if names["SlackPost"] {
		t.Error("DisallowedTools must filter session-scoped SlackPost")
	}
	if !names["SlackListChannels"] {
		t.Error("the other Slack tools must remain")
	}
}

// End to end through the REAL slack.Run against a fake Slack API: while it
// runs, the agent-wide registry never gains Slack tools; after ctx is
// cancelled, Run returns and the caller-owned agent is still open and clean.
func TestRunNeverTouchesAgentWideToolsAndKeepsAgentOpen(t *testing.T) {
	isolateConfig(t)
	t.Setenv("HOME", t.TempDir())
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
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	if err := agent.NewOpenAIProvider("slack-run-e2e", models.URL); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: agentstore.NewInMemoryStore()})
	defer a.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, a, WithWorkspace(slackAPI.URL), WithXoxC("xoxc"), WithXoxD("xoxd"),
			WithSessionModel("slack-run-e2e/m"), WithCWD("/x"))
	}()
	time.Sleep(500 * time.Millisecond) // past AuthTest + server start

	mid, err := a.NewSession("/cards", "slack-run-e2e/m")
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

	after, err := a.NewSession("/cards", "slack-run-e2e/m")
	if err != nil {
		t.Fatalf("agent closed by slack.Run: %v", err)
	}
	if hasAnySlackTool(toolNames(t, after)) {
		t.Error("Slack tools left behind after Run returned")
	}
}
