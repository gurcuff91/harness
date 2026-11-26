package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/profiles"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// GET /api/sessions/{id}/sysprompt returns the session's system prompt as
// text/markdown (directives included); the SDK client returns the same text.
func TestSessionSystemPromptEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	if err := admin.RegisterOpenAI("srv-sysprompt-fake", models.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("srv-sysprompt-fake") })
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	ts := httptest.NewServer(NewServer(a, ServerOptions{Logger: logx.NewNilLogger()}).handler())
	defer ts.Close()
	c := client.New(ts.URL)

	const d = "## Sysprompt marker\n\nOnly here."
	if err := a.RegisterProfile("marker", profiles.Profile{Directives: []string{d}}); err != nil {
		t.Fatal(err)
	}
	sess, err := c.CreateSession("srv-sysprompt-fake/m", "/p", "", client.SessionWithEnabledProfiles("marker"))
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

// KeepAgentOpen: closing a frontend's in-process server closes its own
// sessions but leaves the caller's agent usable; without it, Close closes
// the agent (server.Run's ownership semantics, unchanged).
func TestKeepAgentOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	if err := admin.RegisterOpenAI("srv-keepopen-fake", models.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider("srv-keepopen-fake") })
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
		if err := admin.RegisterOpenAI(name, models.URL, admin.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
			return []types.ModelMeta{{ID: "m", ContextWindow: 1_000_000, MaxTokens: max}}, nil
		})); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.UnregisterProvider(name) })
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

// POST /api/settings/provider/reload through the client: the embedder's flow
// — PutCustomProvider, then ReloadCustomProviders — makes the provider live
// (it appears in /api/models) without a restart; deleting + reloading drops it.
func TestReloadCustomProvidersEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c := client.New(srv.Addr())
	providers.EnsureRegistry() // the registry is built (from settings) BEFORE the PUT below
	listed := func() bool {
		ms, _ := c.ListModels()
		for _, m := range ms {
			if m.Model == "ep-live/m" {
				return true
			}
		}
		return false
	}

	if _, err := c.PutCustomProvider("ep-live", types.CustomProvider{Type: "openai", URL: models.URL}); err != nil {
		t.Fatal(err)
	}
	if listed() {
		t.Fatal("precondition: not live before the reload")
	}
	st, err := c.ReloadCustomProviders()
	if err != nil || st.Code != "reloaded" {
		t.Fatalf("reload = %+v, %v", st, err)
	}
	if !listed() {
		t.Fatal("after reload the provider's models must be listed")
	}

	c.DeleteCustomProvider("ep-live")
	c.ReloadCustomProviders()
	if listed() {
		t.Fatal("after delete + reload the provider must be gone")
	}

	// Conflict with a programmatic provider: reported, not fatal.
	if err := admin.RegisterOpenAI("ep-code", models.URL); err != nil {
		t.Fatal(err)
	}
	defer admin.UnregisterProvider("ep-code")
	c.PutCustomProvider("ep-code", types.CustomProvider{Type: "openai", URL: models.URL})
	st, err = c.ReloadCustomProviders()
	if err != nil || st.Code != "reloaded_with_conflicts" || !strings.Contains(st.Message, "ep-code") {
		t.Fatalf("conflict reload = %+v, %v", st, err)
	}
}

// POST /api/sessions, /resume and /fork accept "directives" for THAT session
// only; they are not stored, and an empty body still works.
func TestSessionDirectivesOverHTTP(t *testing.T) {
	srv, a, model := sharedServer(t, nil)
	c := client.New(srv.Addr())
	const d = "## Only for this session"
	prompt := func(id string) string {
		s, err := a.ResumeSession(id)
		if err != nil {
			t.Fatal(err)
		}
		return s.SystemPrompt()
	}

	with, err := c.CreateSession(model, "/a", "", client.SessionWithDirectives(d))
	if err != nil {
		t.Fatal(err)
	}
	without, _ := c.CreateSession(model, "/b", "")
	if !strings.Contains(prompt(with.ID), d) || strings.Contains(prompt(without.ID), d) {
		t.Fatal("create: the directive must reach only the session that asked for it")
	}

	c.CloseSession(with.ID)
	if _, err := c.ResumeSession(with.ID, client.SessionWithDirectives(d+" (resumed)")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt(with.ID), d+" (resumed)") {
		t.Error("resume: the directive must reach the reopened session")
	}
	c.CloseSession(with.ID)
	if _, err := c.ResumeSession(with.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt(with.ID), d) {
		t.Error("a resume without the option must not carry it (not stored)")
	}

	f, err := c.ForkSession(without.ID, client.SessionWithDirectives("## Fork text"))
	if err != nil || !strings.Contains(prompt(f.ID), "## Fork text") {
		t.Fatalf("fork: %v", err)
	}
	f2, _ := c.ForkSession(f.ID)
	if strings.Contains(prompt(f2.ID), "## Fork text") {
		t.Error("a fork must not inherit its parent's directives")
	}
}
