package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// directivesTestAgent returns an agent backed by a fake OpenAI-compatible
// provider (no network) and the "provider/model" id to open sessions with.
func directivesTestAgent(t *testing.T, st store.SessionStore, opts AgentOptions) (*Agent, string) {
	t.Helper()
	withCleanProviderRegistry(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(srv.Close)
	if err := NewOpenAIProvider("directives-fake", srv.URL); err != nil {
		t.Fatal(err)
	}
	opts.Store = st
	opts.ResourceLoader = resources.NilLoader{}
	a := New(opts)
	t.Cleanup(func() { a.Close() })
	return a, "directives-fake/m"
}

const tgDirective = "## Telegram\n\nYou are talking over Telegram."

// Criterion 3: a session's directives reach only THAT session's prompt.
func TestSessionDirectivesAreScopedToTheSession(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{})

	tg, err := a.NewSession("/tg", model, WithSessionDirectives(tgDirective))
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.NewSession("/elsewhere", model)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tg.systemPrompt, tgDirective) {
		t.Error("the Telegram session's prompt must include its directive")
	}
	if strings.Contains(other.systemPrompt, tgDirective) {
		t.Error("a session created without it must not see the directive")
	}
}

// Agent-wide directives come first, session ones after; an identical block at
// both levels appears once.
func TestSessionDirectivesOrderAndDedup(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{Directives: []string{"## Agent wide", tgDirective}})
	s, err := a.NewSession("/p", model, WithSessionDirectives(tgDirective, "## Session only"))
	if err != nil {
		t.Fatal(err)
	}
	p := s.systemPrompt
	if strings.Count(p, tgDirective) != 1 {
		t.Errorf("duplicate directive appears %d times, want 1", strings.Count(p, tgDirective))
	}
	if i, j := strings.Index(p, "## Agent wide"), strings.Index(p, "## Session only"); i < 0 || j < 0 || i > j {
		t.Errorf("want agent-wide (%d) before session-only (%d)", i, j)
	}
}

// Criterion 4: directives aren't persisted — a session resumed after a
// "restart" (a new Agent over the same store) gets them from whoever resumes
// it, and only then.
func TestSessionDirectivesOnResumeAfterRestart(t *testing.T) {
	st := store.NewInMemoryStore()
	a1, model := directivesTestAgent(t, st, AgentOptions{})
	s, err := a1.NewSession("/tg", model, WithSessionDirectives(tgDirective))
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()

	a2 := New(AgentOptions{Store: st, ResourceLoader: resources.NilLoader{}})
	defer a2.Close()
	resumed, err := a2.ResumeSession(id, WithSessionDirectives(tgDirective))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumed.systemPrompt, tgDirective) {
		t.Error("a session resumed with the directive must include it")
	}
	resumed.Close()

	plain, err := a2.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.systemPrompt, tgDirective) {
		t.Error("directives must not be persisted — a plain resume (e.g. the TUI) must not get them")
	}
}

// Option (a): resuming an ALREADY-ACTIVE session ignores directives — the
// live session keeps the prompt it was opened with.
func TestResumeActiveSessionIgnoresDirectives(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{})
	s, err := a.NewSession("/p", model)
	if err != nil {
		t.Fatal(err)
	}
	same, err := a.ResumeSession(s.ID(), WithSessionDirectives(tgDirective))
	if err != nil {
		t.Fatal(err)
	}
	if same != s {
		t.Fatal("resuming an active session must return the live handle")
	}
	if strings.Contains(same.systemPrompt, tgDirective) {
		t.Error("an active session's prompt must not change on resume")
	}
}

// WithSessionTools: scoped to the session, filtered by DisallowedTools,
// replaces an agent-wide tool of the same name in that session only, and is
// not persisted.
func TestSessionToolsScopedFilteredAndNotPersisted(t *testing.T) {
	st := store.NewInMemoryStore()
	mk := func(name, desc string) tools.Tool {
		return tools.Tool{Def: types.ToolDef{Name: name, Description: desc, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	}
	a, model := directivesTestAgent(t, st, AgentOptions{
		DisallowedTools: []string{"Blocked"},
		Tools:           []tools.Tool{mk("Shared", "agent-wide")},
	})
	defs := func(s *Session) map[string]string {
		out := map[string]string{}
		for _, d := range s.Tools() {
			out[d.Name] = d.Description
		}
		return out
	}

	s, err := a.NewSession("/p", model, WithSessionTools(mk("Mine", "x"), mk("Blocked", "x"), mk("Shared", "session")))
	if err != nil {
		t.Fatal(err)
	}
	got := defs(s)
	if got["Mine"] == "" || got["Blocked"] != "" || got["Shared"] != "session" {
		t.Errorf("session tools = %v; want Mine, no Blocked, Shared overridden", got)
	}
	other, _ := a.NewSession("/q", model)
	if og := defs(other); og["Mine"] != "" || og["Shared"] != "agent-wide" {
		t.Errorf("another session = %v; want no Mine and the agent-wide Shared", og)
	}

	id := s.ID()
	s.Close()
	plain, _ := a.ResumeSession(id)
	if defs(plain)["Mine"] != "" {
		t.Error("session tools must not be persisted across resume")
	}
}
