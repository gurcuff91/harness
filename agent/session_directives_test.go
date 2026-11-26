package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/profiles"
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
	if opts.ResourceLoader == nil {
		opts.ResourceLoader = resources.NilLoader{}
	}
	a := New(opts)
	t.Cleanup(func() { a.Close() })
	return a, "directives-fake/m"
}

const tgDirective = "## Telegram\n\nYou are talking over Telegram."

// The agent's directives reach every session; a profile's reach only the
// sessions that enabled it. There is no per-session directive any more.
func TestDirectivesAreTheAgentsAndProfilesScopeTheRest(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{Directives: []string{"## Agent wide"}})
	if err := a.RegisterProfile("tg", profiles.Profile{Directives: []string{tgDirective}}); err != nil {
		t.Fatal(err)
	}
	tg, err := a.NewSession("/tg", model, SessionWithEnabledProfiles("tg"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.NewSession("/elsewhere", model)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Session{tg, other} {
		if !strings.Contains(s.systemPrompt, "## Agent wide") {
			t.Error("the agent's directive must reach every session")
		}
	}
	if !strings.Contains(tg.systemPrompt, tgDirective) || strings.Contains(other.systemPrompt, tgDirective) {
		t.Error("the profile's directive must reach only the session that enabled it")
	}
}

// Order: agent directives first, then the profiles'; an identical block at
// both levels appears once.
func TestDirectivesOrderAndDedup(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{Directives: []string{"## Agent wide", tgDirective}})
	_ = a.RegisterProfile("p", profiles.Profile{Directives: []string{tgDirective, "## Profile only"}})
	s, err := a.NewSession("/p", model, SessionWithEnabledProfiles("p"))
	if err != nil {
		t.Fatal(err)
	}
	p := s.systemPrompt
	if strings.Count(p, tgDirective) != 1 {
		t.Errorf("duplicate directive appears %d times, want 1", strings.Count(p, tgDirective))
	}
	if i, j := strings.Index(p, "## Agent wide"), strings.Index(p, "## Profile only"); i < 0 || j < 0 || i > j {
		t.Errorf("want agent-wide (%d) before the profile's (%d)", i, j)
	}
}

// The profile's directive is applied again by whoever reopens the session (the
// binding is persisted by name), even from a new agent over the same store.
func TestProfileDirectiveSurvivesAReopenAfterRestart(t *testing.T) {
	st := store.NewInMemoryStore()
	a1, model := directivesTestAgent(t, st, AgentOptions{})
	_ = a1.RegisterProfile("tg", profiles.Profile{Directives: []string{tgDirective}})
	s, err := a1.NewSession("/tg", model, SessionWithEnabledProfiles("tg"))
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()

	a2 := New(AgentOptions{Store: st, ResourceLoader: resources.NilLoader{}, Profiles: map[string]profiles.Profile{"tg": {Directives: []string{tgDirective}}}})
	defer a2.Close()
	resumed, err := a2.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumed.systemPrompt, tgDirective) {
		t.Error("a plain resume must re-apply the persisted profile")
	}
}

// Resuming an ALREADY-ACTIVE session without profile options leaves its prompt alone.
func TestResumeActiveSessionKeepsItsPrompt(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{})
	s, err := a.NewSession("/p", model)
	if err != nil {
		t.Fatal(err)
	}
	before := s.systemPrompt
	same, err := a.ResumeSession(s.ID())
	if err != nil || same != s || same.systemPrompt != before {
		t.Fatalf("a resume of an active session must return it untouched (%v)", err)
	}
}

// The agent's tools and DisallowedTools apply to every session; a profile's
// tools only to the sessions that enabled it, still filtered by DisallowedTools.
func TestToolsAreTheAgentsAndProfilesScopeTheRest(t *testing.T) {
	mk := func(name, desc string) tools.Tool {
		return tools.Tool{Def: types.ToolDef{Name: name, Description: desc, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	}
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{
		DisallowedTools: []string{"Blocked"},
		Tools:           []tools.Tool{mk("Shared", "agent-wide")},
	})
	_ = a.RegisterProfile("p", profiles.Profile{Tools: func() []tools.Tool { return []tools.Tool{mk("Mine", "x"), mk("Blocked", "x")} }})
	defs := func(s *Session) map[string]string {
		out := map[string]string{}
		for _, d := range s.Tools() {
			out[d.Name] = d.Description
		}
		return out
	}
	s, err := a.NewSession("/p", model, SessionWithEnabledProfiles("p"))
	if err != nil {
		t.Fatal(err)
	}
	got := defs(s)
	if got["Mine"] == "" || got["Blocked"] != "" || got["Shared"] != "agent-wide" {
		t.Errorf("session tools = %v; want Mine and the agent-wide Shared, no Blocked", got)
	}
	other, _ := a.NewSession("/q", model)
	if og := defs(other); og["Mine"] != "" || og["Shared"] != "agent-wide" {
		t.Errorf("another session = %v; want no Mine and the agent-wide Shared", og)
	}
}

// The base prompt: what the caller set > SYSTEM.md > the built-in default.
type systemMDLoader struct{ resources.NilLoader }

func (systemMDLoader) Load() (*resources.Resources, error) {
	return &resources.Resources{SystemMD: "FROM SYSTEM.MD"}, nil
}
func (l systemMDLoader) Copy() resources.ResourceLoader { return l }

func TestSystemPromptPrecedence(t *testing.T) {
	cases := []struct {
		name, explicit, want, notWant string
		loader                        resources.ResourceLoader
	}{
		{"explicit beats SYSTEM.md", "MINE", "MINE", "FROM SYSTEM.MD", systemMDLoader{}},
		{"SYSTEM.md beats the default", "", "FROM SYSTEM.MD", "expert coding agent", systemMDLoader{}},
		{"the default when there is nothing else", "", "You are an expert coding agent", "FROM SYSTEM.MD", resources.NilLoader{}},
		{"explicit beats the default", "MINE", "MINE", "expert coding agent", resources.NilLoader{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{SystemPrompt: c.explicit, ResourceLoader: c.loader})
			s, err := a.NewSession("/p", model)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(s.systemPrompt, c.want) || strings.Contains(s.systemPrompt, c.notWant) {
				t.Errorf("prompt = %.80q", s.systemPrompt)
			}
		})
	}
}
