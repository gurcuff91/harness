package server

import (
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
