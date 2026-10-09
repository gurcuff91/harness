package telegram

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	agentstore "github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

// captureStdout runs fn and returns everything it wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// Criterion 2: the allowlist API returns values and prints nothing.
func TestAllowlistAPIReturnsValuesSilently(t *testing.T) {
	isolateConfig(t)
	out := captureStdout(t, func() {
		if added, err := PairChat(123); err != nil || !added {
			t.Errorf("PairChat(123) = %v, %v; want true, nil", added, err)
		}
		if added, err := PairChat(123); err != nil || added {
			t.Errorf("PairChat(123) again = %v, %v; want false, nil", added, err)
		}
		if ids, err := PairedChats(); err != nil || len(ids) != 1 || ids[0] != 123 {
			t.Errorf("PairedChats() = %v, %v; want [123]", ids, err)
		}
		if removed, err := UnpairChat(123); err != nil || !removed {
			t.Errorf("UnpairChat(123) = %v, %v; want true, nil", removed, err)
		}
		if removed, err := UnpairChat(123); err != nil || removed {
			t.Errorf("UnpairChat(123) again = %v, %v; want false, nil", removed, err)
		}
	})
	if out != "" {
		t.Errorf("allowlist API wrote to stdout: %q", out)
	}
}

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
	// The provider registry is process-global: one provider name per test.
	provName := "tg-embed-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
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
	srv := server.NewServer(a, server.ServerOptions{Logger: logx.NewNilLogger(), Transport: "telegram"})
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
		pumps:  map[int64]*chatPump{},
	}, a
}

// Criteria 1 + 3: WithCWD decides where the chat's session lives (and where
// its binding is stored), and the transport — not the agent — adds
// telegram.Directive, so a session created outside it doesn't get it.
func TestEmbeddedTransportCWDAndSessionDirective(t *testing.T) {
	tr, a := embedTransport(t, Options{CWD: "/x"})

	id, err := tr.acquireSession(42)
	if err != nil {
		t.Fatalf("acquireSession: %v", err)
	}
	sess, err := a.ResumeSession(id) // already active → the live handle
	if err != nil {
		t.Fatal(err)
	}
	if sess.CWD() != "/x" {
		t.Errorf("session cwd = %q, want /x", sess.CWD())
	}
	if got, _ := ChatSessions("/x"); got[42] != id {
		t.Errorf("ChatSessions(/x) = %v, want chat 42 → %s", got, id)
	}
	if !strings.Contains(sess.SystemPrompt(), Directive) {
		t.Error("the Telegram session's prompt must include telegram.Directive")
	}

	other, err := tr.api.CreateSession(tr.model, "/elsewhere", "")
	if err != nil {
		t.Fatal(err)
	}
	o, _ := a.ResumeSession(other.ID)
	if strings.Contains(o.SystemPrompt(), Directive) {
		t.Error("a session created outside the transport must not include telegram.Directive")
	}
}

// Criterion 4: after a restart, the transport resumes the chat's stored
// session WITH its directive (directives aren't persisted — it re-adds them).
func TestEmbeddedTransportResumeKeepsDirective(t *testing.T) {
	tr, a := embedTransport(t, Options{CWD: "/x"})
	id, err := tr.acquireSession(7)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := a.ResumeSession(id); s != nil {
		s.Close() // drop the live session: the next acquire must reopen it from the store
	}
	if _, err := tr.api.CloseSession(id); err != nil {
		t.Logf("close via api: %v", err)
	}

	again, err := tr.acquireSession(7)
	if err != nil || again != id {
		t.Fatalf("acquireSession after close = %q, %v; want the stored %q", again, err, id)
	}
	s, _ := a.ResumeSession(id)
	if !strings.Contains(s.SystemPrompt(), Directive) {
		t.Error("a resumed Telegram session must include telegram.Directive")
	}
}
