package agent

import (
	"bytes"
	"encoding/json"
	"github.com/gurcuff91/harness/admin"
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
	if err := admin.RegisterOpenAI("directives-fake", srv.URL); err != nil {
		t.Fatal(err)
	}
	opts.Store = st
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

	a2 := New(AgentOptions{Store: st, ResourceLoader: resources.Nil(), Profiles: map[string]profiles.Profile{"tg": {Directives: []string{tgDirective}}}})
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
type systemMDLoader struct{}

func (systemMDLoader) Load(string) (*resources.Resources, error) {
	return &resources.Resources{SystemMD: "FROM SYSTEM.MD"}, nil
}

func TestSystemPromptPrecedence(t *testing.T) {
	cases := []struct {
		name, explicit, want, notWant string
		loader                        resources.ResourceLoader
	}{
		{"explicit beats SYSTEM.md", "MINE", "MINE", "FROM SYSTEM.MD", systemMDLoader{}},
		{"SYSTEM.md beats the default", "", "FROM SYSTEM.MD", "expert coding agent", systemMDLoader{}},
		{"the default when there is nothing else", "", "You are an expert coding agent", "FROM SYSTEM.MD", nil},
		{"explicit beats the default", "MINE", "MINE", "expert coding agent", resources.Nil()},
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

// SessionWithDirectives: text particular to ONE session. Order in the prompt:
// the agent's, the enabled profiles', the session's own.
func TestSessionDirectivesAreScopedAndOrdered(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{Directives: []string{"## Agent wide"}})
	_ = a.RegisterProfile("p", profiles.Profile{Directives: []string{"## Profile text"}})

	s, err := a.NewSession("/a", model, SessionWithEnabledProfiles("p"), SessionWithDirectives("## Own A", "  ", "## Own A"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := a.NewSession("/b", model, SessionWithDirectives("## Own B"))
	plain, _ := a.NewSession("/c", model)

	p := s.systemPrompt
	i, j, k := strings.Index(p, "## Agent wide"), strings.Index(p, "## Profile text"), strings.Index(p, "## Own A")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("order agent(%d) < profile(%d) < own(%d) violated", i, j, k)
	}
	if strings.Count(p, "## Own A") != 1 {
		t.Error("an identical block must appear once; a blank one must be dropped")
	}
	if strings.Contains(other.systemPrompt, "## Own A") || !strings.Contains(other.systemPrompt, "## Own B") {
		t.Error("two sessions with different directives must not touch each other")
	}
	if strings.Contains(plain.systemPrompt, "## Own") {
		t.Error("a session without directives is unchanged")
	}
}

// Not persisted: whoever reopens passes them again; a fork does not inherit.
func TestSessionDirectivesAreNotPersistedNorInherited(t *testing.T) {
	st := store.NewInMemoryStore()
	a1, model := directivesTestAgent(t, st, AgentOptions{})
	s, _ := a1.NewSession("/a", model, SessionWithDirectives("## Mine"))
	id := s.ID()

	f, err := a1.ForkSession(id)
	if err != nil || strings.Contains(f.systemPrompt, "## Mine") {
		t.Fatalf("a fork must not inherit the parent's directives (%v)", err)
	}
	f2, err := a1.ForkSession(id, SessionWithDirectives("## Fork own"))
	if err != nil || !strings.Contains(f2.systemPrompt, "## Fork own") || strings.Contains(f2.systemPrompt, "## Mine") {
		t.Fatalf("a fork takes the ones it is given (%v)", err)
	}
	s.Close()

	r, _ := a1.ResumeSession(id)
	if strings.Contains(r.systemPrompt, "## Mine") {
		t.Fatal("a plain reopen must not carry them: they are not stored")
	}
	r.Close()
	r, err = a1.ResumeSession(id, SessionWithDirectives("## Again"))
	if err != nil || !strings.Contains(r.systemPrompt, "## Again") {
		t.Fatalf("a reopen with the option carries them (%v)", err)
	}
	// An already-open session keeps the prompt it has.
	same, _ := a1.ResumeSession(id, SessionWithDirectives("## Ignored"))
	if same != r || strings.Contains(same.systemPrompt, "## Ignored") {
		t.Error("resuming an open session must not change its prompt")
	}
}

// A live reconfiguration (a profile registered or removed) keeps the session's own text.
func TestSessionDirectivesSurviveALiveReconfigure(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{})
	_ = a.RegisterProfile("p", profiles.Profile{Directives: []string{"## P1"}})
	s, _ := a.NewSession("/a", model, SessionWithEnabledProfiles("p"), SessionWithDirectives("## Own"))
	_ = a.RegisterProfile("p", profiles.Profile{Directives: []string{"## P2"}})
	if !strings.Contains(s.SystemPrompt(), "## P2") || !strings.Contains(s.SystemPrompt(), "## Own") {
		t.Fatalf("reconfigure lost the session's own directive:\n%s", s.SystemPrompt())
	}
}

// An import does not bring directives (they are not in the export) and takes the ones it is given.
func TestImportTakesTheDirectivesItIsGiven(t *testing.T) {
	a, model := profAgent(t)
	s, _ := a.NewSession(t.TempDir(), model, SessionWithDirectives("## Original"))
	var buf bytes.Buffer
	if _, err := a.ExportSession(s.ID(), &buf); err != nil {
		t.Fatal(err)
	}
	im, err := a.ImportSession(bytes.NewReader(buf.Bytes()), "", SessionWithDirectives("## For the copy"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(im.systemPrompt, "## Original") || !strings.Contains(im.systemPrompt, "## For the copy") {
		t.Fatalf("import prompt wrong:\n%s", im.systemPrompt)
	}
}
