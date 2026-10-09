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
