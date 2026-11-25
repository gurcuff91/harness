package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/profiles"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

func profTool(name string) tools.Tool {
	return tools.Tool{Def: types.ToolDef{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func profNames(s *Session) []string {
	var out []string
	for _, d := range s.Tools() {
		if strings.HasPrefix(d.Name, "Prof") {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return out
}

func profAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{})
	for _, n := range []string{"chat", "other"} {
		if err := a.RegisterProfile(n, profiles.Profile{
			Directives: []string{"## " + n + " directive"},
			Tools:      func() []tools.Tool { return []tools.Tool{profTool("Prof_" + n)} },
		}); err != nil {
			t.Fatal(err)
		}
	}
	return a, model
}

func TestProfilesReachOnlyBoundSessions(t *testing.T) {
	a, model := profAgent(t)
	b, err := a.NewSession("/p", model, SessionWithProfiles("chat"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.NewSession("/q", model)
	if !strings.Contains(b.SystemPrompt(), "## chat directive") || !slices.Equal(profNames(b), []string{"Prof_chat"}) {
		t.Fatal("bound session lacks the profile")
	}
	if strings.Contains(p.SystemPrompt(), "## chat directive") || len(profNames(p)) != 0 {
		t.Fatal("unbound session got it")
	}
	if got := a.SessionProfiles(b.ID()); !slices.Equal(got, []string{"chat"}) {
		t.Fatalf("persisted = %v", got)
	}
}

func TestNewSessionWithUnknownProfileFailsAndLeavesNothing(t *testing.T) {
	a, model := profAgent(t)
	_, err := a.NewSession("/p", model, SessionWithProfiles("chat", "nope"))
	if !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("err = %v", err)
	}
	if metas, _ := a.ListAllSessions(); len(metas) != 0 {
		t.Fatalf("left %d sessions behind", len(metas))
	}
	if _, err := a.NewSession("/p", model, SessionWithProfiles("chat", " ")); err == nil {
		t.Fatal("blank name must be an error")
	}
}

func TestResumePatchSemantics(t *testing.T) {
	a, model := profAgent(t)
	s, _ := a.NewSession("/p", model, SessionWithProfiles("chat"))
	id := s.ID()
	s.Close()

	r, err := a.ResumeSession(id) // not passed → unchanged
	if err != nil || !slices.Equal(a.SessionProfiles(id), []string{"chat"}) || !slices.Equal(profNames(r), []string{"Prof_chat"}) {
		t.Fatalf("unchanged: %v %v", err, a.SessionProfiles(id))
	}
	r.Close()
	r, err = a.ResumeSession(id, SessionWithProfiles("other")) // replace
	if err != nil || !slices.Equal(a.SessionProfiles(id), []string{"other"}) || !slices.Equal(profNames(r), []string{"Prof_other"}) {
		t.Fatalf("replace: %v %v %v", err, a.SessionProfiles(id), profNames(r))
	}
	// Live session: passing rebinds (applies at once, idle).
	if _, err := a.ResumeSession(id, SessionWithProfiles("chat", "other")); err != nil {
		t.Fatal(err)
	}
	if got := profNames(r); !slices.Equal(got, []string{"Prof_chat", "Prof_other"}) {
		t.Fatalf("live rebind: %v", got)
	}
	if _, err := a.ResumeSession(id, SessionWithProfiles("nope")); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("unknown on live = %v", err)
	}
	if _, err := a.ResumeSession(id, SessionWithProfiles()); err != nil { // none → remove all
		t.Fatal(err)
	}
	if len(a.SessionProfiles(id)) != 0 || len(profNames(r)) != 0 {
		t.Fatalf("remove all: %v %v", a.SessionProfiles(id), profNames(r))
	}
}

func TestMissingStoredProfileIsSkippedKeptAndReported(t *testing.T) {
	a, model := profAgent(t)
	s, _ := a.NewSession("/p", model, SessionWithProfiles("chat", "other"))
	id := s.ID()

	a.UnregisterProfile("chat") // live session reconfigured
	if got := s.MissingProfiles(); !slices.Equal(got, []string{"chat"}) {
		t.Fatalf("live missing = %v", got)
	}
	if strings.Contains(s.SystemPrompt(), "## chat directive") || !strings.Contains(s.SystemPrompt(), "## other directive") {
		t.Fatal("unregistered profile must leave the live prompt")
	}
	s.Close()
	r, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.MissingProfiles(), []string{"chat"}) || !slices.Equal(a.SessionProfiles(id), []string{"chat", "other"}) {
		t.Fatalf("missing %v kept %v", r.MissingProfiles(), a.SessionProfiles(id))
	}
	// Registering it again reconfigures the live session.
	_ = a.RegisterProfile("chat", profiles.Profile{Directives: []string{"## chat directive"}})
	if len(r.MissingProfiles()) != 0 || !strings.Contains(r.SystemPrompt(), "## chat directive") {
		t.Fatal("re-registering must apply to the live session")
	}
}

func TestForkInheritsAndReplacesProfiles(t *testing.T) {
	a, model := profAgent(t)
	s, _ := a.NewSession("/p", model, SessionWithProfiles("chat"))
	f, err := a.ForkSession(s.ID())
	if err != nil || !slices.Equal(a.SessionProfiles(f.ID()), []string{"chat"}) || !slices.Equal(profNames(f), []string{"Prof_chat"}) {
		t.Fatalf("inherit: %v %v", err, a.SessionProfiles(f.ID()))
	}
	f2, err := a.ForkSession(s.ID(), SessionWithProfiles("other"))
	if err != nil || !slices.Equal(a.SessionProfiles(f2.ID()), []string{"other"}) || !slices.Equal(profNames(f2), []string{"Prof_other"}) {
		t.Fatalf("replace: %v %v", err, a.SessionProfiles(f2.ID()))
	}
	if !slices.Equal(a.SessionProfiles(s.ID()), []string{"chat"}) {
		t.Fatal("parent changed")
	}
	n := len(mustList(t, a))
	if _, err := a.ForkSession(s.ID(), SessionWithProfiles("nope")); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("unknown = %v", err)
	}
	if len(mustList(t, a)) != n {
		t.Fatal("failed fork left a session")
	}
	a.UnregisterProfile("chat")
	f3, err := a.ForkSession(s.ID()) // stored name missing: skipped, not a failure
	if err != nil || !slices.Equal(f3.MissingProfiles(), []string{"chat"}) {
		t.Fatalf("missing on fork: %v %v", err, f3)
	}
}

func mustList(t *testing.T, a *Agent) []store.SessionMeta {
	t.Helper()
	m, err := a.ListAllSessions()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestImportBringsProfileNames(t *testing.T) {
	a, model := profAgent(t)
	s, _ := a.NewSession(t.TempDir(), model, SessionWithProfiles("chat", "other"))
	var buf bytes.Buffer
	if _, err := a.ExportSession(s.ID(), &buf); err != nil {
		t.Fatal(err)
	}
	a.UnregisterProfile("other")
	im, err := a.ImportSession(bytes.NewReader(buf.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.SessionProfiles(im.ID()), []string{"chat", "other"}) || !slices.Equal(im.MissingProfiles(), []string{"other"}) {
		t.Fatalf("import: %v %v", a.SessionProfiles(im.ID()), im.MissingProfiles())
	}
	if !slices.Equal(profNames(im), []string{"Prof_chat"}) {
		t.Fatalf("tools = %v", profNames(im))
	}
}

func TestProfilesFromAgentOptions(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{
		Profiles: map[string]profiles.Profile{"x": {Directives: []string{"## x"}}},
	})
	s, err := a.NewSession("/p", model, SessionWithProfiles("x"))
	if err != nil || !strings.Contains(s.SystemPrompt(), "## x") {
		t.Fatalf("%v", err)
	}
	if !slices.Equal(a.ProfileNames(), []string{"x"}) {
		t.Fatal(a.ProfileNames())
	}
}
