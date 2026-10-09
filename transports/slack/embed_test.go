package slack

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	agentstore "github.com/gurcuff91/harness/agent/store"
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

	st, _ := openStore(opts.CWD)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := server.NewServer(a, server.ServerOptions{Logger: logx.NewNilLogger(), Transport: "slack"})
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return &Transport{
		opts:   opts,
		agent:  a,
		api:    client.New(ln.Addr().String()),
		store:  st,
		srv:    srv,
		logger: logx.NewNilLogger(),
		model:  provName + "/m",
		cwd:    st.cwd,
		pumps:  map[string]*channelPump{},
	}, a
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
