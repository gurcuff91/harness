package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// POST /api/sessions and /resume accept optional "directives" scoped to that
// session; the resume body stays optional (empty body = none).
func TestSessionDirectivesOverHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	if err := agent.NewOpenAIProvider("srv-directives-fake", models.URL); err != nil {
		t.Fatal(err)
	}
	st := store.NewInMemoryStore()
	a := agent.New(agent.AgentOptions{Store: st, ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	ts := httptest.NewServer(NewServer(a, ServerOptions{Logger: logx.NewNilLogger()}).handler())
	defer ts.Close()
	c := client.New(ts.URL)
	const d = "## Only for this session"

	with, err := c.CreateSession("srv-directives-fake/m", "/a", "", client.WithDirectives(d))
	if err != nil {
		t.Fatal(err)
	}
	without, err := c.CreateSession("srv-directives-fake/m", "/b", "")
	if err != nil {
		t.Fatal(err)
	}
	prompt := func(id string) string {
		s, err := a.ResumeSession(id)
		if err != nil {
			t.Fatal(err)
		}
		return s.SystemPrompt()
	}
	if !strings.Contains(prompt(with.ID), d) || strings.Contains(prompt(without.ID), d) {
		t.Fatal("create: directives must reach only the session that asked for them")
	}

	// Resume after the session is closed: body with directives, then none.
	c.CloseSession(with.ID)
	if _, err := c.ResumeSession(with.ID, client.WithDirectives(d+" (resumed)")); err != nil {
		t.Fatalf("resume with directives: %v", err)
	}
	if !strings.Contains(prompt(with.ID), d+" (resumed)") {
		t.Error("resume: directives must reach the reopened session")
	}
	c.CloseSession(with.ID)
	if _, err := c.ResumeSession(with.ID); err != nil {
		t.Fatalf("resume with no body: %v", err)
	}
	if strings.Contains(prompt(with.ID), d) {
		t.Error("resume without directives must not carry them (not persisted)")
	}
}

// GET /api/sessions/{id}/sysprompt returns the session's system prompt as
// text/markdown (directives included); the SDK client returns the same text.
func TestSessionSystemPromptEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	if err := agent.NewOpenAIProvider("srv-sysprompt-fake", models.URL); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	ts := httptest.NewServer(NewServer(a, ServerOptions{Logger: logx.NewNilLogger()}).handler())
	defer ts.Close()
	c := client.New(ts.URL)

	const d = "## Sysprompt marker\n\nOnly here."
	sess, err := c.CreateSession("srv-sysprompt-fake/m", "/p", "", client.WithDirectives(d))
	if err != nil {
		t.Fatal(err)
	}
	live, _ := a.ResumeSession(sess.ID)

	resp, err := http.Get(ts.URL + "/api/sessions/" + sess.ID + "/sysprompt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/markdown") {
		t.Fatalf("status %d, content-type %q; want 200 text/markdown", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if string(body) != live.SystemPrompt() || !strings.Contains(string(body), d) {
		t.Fatalf("body is not the session's system prompt (len %d vs %d)", len(body), len(live.SystemPrompt()))
	}

	got, err := c.GetSystemPrompt(sess.ID)
	if err != nil || got != live.SystemPrompt() {
		t.Fatalf("client.GetSystemPrompt = %d bytes, %v; want the session's prompt", len(got), err)
	}

	// Inactive/unknown session → 400 *client.Error.
	if _, err := c.GetSystemPrompt("00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("unknown session must error")
	}
}

// KeepAgentOpen: closing a transport's in-process server closes its own
// sessions but leaves the caller's agent usable; without it, Close closes
// the agent (server.Run's ownership semantics, unchanged).
func TestKeepAgentOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	if err := agent.NewOpenAIProvider("srv-keepopen-fake", models.URL); err != nil {
		t.Fatal(err)
	}
	mem := &closeCounter{SessionStore: store.NewInMemoryStore()}
	a := agent.New(agent.AgentOptions{Store: mem, ResourceLoader: resources.NilLoader{}})
	defer a.Close()

	srv := NewServer(a, ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	ts := httptest.NewServer(srv.handler())
	c := client.New(ts.URL)
	sess, err := c.CreateSession("srv-keepopen-fake/m", "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	ts.Close()
	srv.Close()

	if mem.closed != 0 {
		t.Fatal("KeepAgentOpen server closed the agent (its store)")
	}
	// The server's own sessions were closed: resuming yields a fresh object,
	// not the live one (a still-active session would be returned as-is).
	again, err := a.ResumeSession(sess.ID)
	if err != nil {
		t.Fatalf("resume after server close: %v", err)
	}
	again.Close()
	if _, err := a.NewSession("/q", "srv-keepopen-fake/m"); err != nil {
		t.Fatalf("agent must remain usable: %v", err)
	}

	owned := NewServer(a, ServerOptions{Logger: logx.NewNilLogger()})
	owned.Close()
	if mem.closed != 1 {
		t.Fatalf("default server must close the agent (store closed %d times)", mem.closed)
	}
}

// closeCounter is an in-memory session store that counts Close calls — the
// agent closes its store in Agent.Close, so this observes agent shutdown.
type closeCounter struct {
	store.SessionStore
	closed int
}

func (c *closeCounter) Close() error { c.closed++; return nil }

// Part 2: max_tokens on GET /api/sessions/{id} and /info — the effective
// ceiling, for active and persisted sessions, following the three resolution
// paths and a model switch.
func TestSessionMaxTokensInAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	// Catalogs: "big" says 512k (capped to 64k), "small" says 8k, "none" unknown.
	reg := func(name string, max int) {
		agent.NewOpenAIProvider(name, models.URL, agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
			return []types.ModelMeta{{ID: "m", ContextWindow: 1_000_000, MaxTokens: max}}, nil
		}))
	}
	reg("mt-big", 512000)
	reg("mt-small", 8000)
	reg("mt-none", 0)

	run := func(t *testing.T, opts agent.AgentOptions, model string, want int) (*client.Client, *client.Session) {
		t.Helper()
		opts.Store, opts.ResourceLoader = store.NewInMemoryStore(), resources.NilLoader{}
		a := agent.New(opts)
		t.Cleanup(func() { a.Close() })
		srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { srv.Close() })
		c := client.New(srv.Addr())
		sess, err := c.CreateSession(model, "/p", "")
		if err != nil {
			t.Fatal(err)
		}
		if sess.MaxTokens != want {
			t.Errorf("create: max_tokens = %d, want %d", sess.MaxTokens, want)
		}
		got, _ := c.GetSession(sess.ID)
		info, _ := c.GetSessionInfo(sess.ID)
		if got.MaxTokens != want || info.Session.MaxTokens != want {
			t.Errorf("GET = %d, /info = %d; want %d", got.MaxTokens, info.Session.MaxTokens, want)
		}
		return c, sess
	}

	t.Run("catalog capped at the default", func(t *testing.T) { run(t, agent.AgentOptions{}, "mt-big/m", 64000) })
	t.Run("catalog below the cap", func(t *testing.T) { run(t, agent.AgentOptions{}, "mt-small/m", 8000) })
	t.Run("unknown → fallback", func(t *testing.T) { run(t, agent.AgentOptions{}, "mt-none/m", 32000) })
	t.Run("explicit wins", func(t *testing.T) { run(t, agent.AgentOptions{MaxTokens: 32000}, "mt-big/m", 32000) })

	t.Run("model switch re-resolves", func(t *testing.T) {
		c, sess := run(t, agent.AgentOptions{}, "mt-big/m", 64000)
		if _, err := c.ExecCommand(sess.ID, "model", map[string]any{"model": "mt-small/m"}); err != nil {
			t.Fatal(err)
		}
		info, _ := c.GetSessionInfo(sess.ID)
		if info.Session.MaxTokens != 8000 {
			t.Errorf("after /model: max_tokens = %d, want 8000", info.Session.MaxTokens)
		}
	})

	t.Run("persisted session", func(t *testing.T) {
		c, sess := run(t, agent.AgentOptions{}, "mt-big/m", 64000)
		c.CloseSession(sess.ID)
		got, err := c.GetSession(sess.ID)
		if err != nil || got.MaxTokens != 64000 {
			t.Errorf("persisted: max_tokens = %d, %v; want 64000", got.MaxTokens, err)
		}
	})
}
