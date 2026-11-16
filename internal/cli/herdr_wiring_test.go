package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// herdrFakeSeq makes each fake provider's name unique, so a test that opens several sessions never
// registers one twice (the registry is process-global).
var herdrFakeSeq int

// herdrSectionMarker is the section's own first sentence. Not the word "Herdr": the repository's own
// AGENTS.md, which every agent started here loads as project context, mentions Herdr.
const herdrSectionMarker = "You are running inside Herdr, a workspace"

// promptOf opens a real session on an agent and returns the system prompt the model would receive.
func promptOf(t *testing.T, a *agent.Agent) string {
	t.Helper()
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(models.Close)
	herdrFakeSeq++
	name := fmt.Sprintf("cli-herdr-fake-%d", herdrFakeSeq)
	if err := agent.NewOpenAIProvider(name, models.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(name) })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := a.NewSession(cwd, name+"/m")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	return sess.SystemPrompt()
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
}

// The wiring, end to end through the agent the interactive TUI really builds: inside Herdr the
// session's system prompt carries the section; outside it does not.
func TestTheTUIAgentCarriesTheHerdrSectionOnlyInsideHerdr(t *testing.T) {
	isolate(t)

	t.Setenv("HC_TERMINAL_ID", "t16")
	a := (&tuiCmd{}).newTUIAgent() // exactly the agent tuiCmd.Run drives
	defer a.Close()
	in := promptOf(t, a)
	for _, want := range []string{"\n## Herdr\n", herdrSectionMarker, "$HC_TERMINAL_ID", `<hc from="ADDRESS name">`, "hc --skill"} {
		if !strings.Contains(in, want) {
			t.Errorf("inside Herdr the system prompt lacks %q", want)
		}
	}
	// it comes AFTER the project context, like every caller-supplied directive
	if i, j := strings.Index(in, "## Working Directory"), strings.Index(in, "\n## Herdr\n"); i < 0 || j < i {
		t.Errorf("the Herdr section (at %d) is not after the Working Directory section (at %d)", j, i)
	}
	if strings.Count(in, herdrSectionMarker) != 1 {
		t.Errorf("the section appears %d times", strings.Count(in, herdrSectionMarker))
	}

	t.Setenv("HC_TERMINAL_ID", "")
	b := (&tuiCmd{}).newTUIAgent()
	defer b.Close()
	if out := promptOf(t, b); strings.Contains(out, herdrSectionMarker) {
		t.Errorf("outside Herdr the system prompt carries the Herdr section")
	}
}

// Every other way harness builds an agent stays free of it, even INSIDE Herdr: none of them receives
// the envelope. newInteractiveAgent is shared with serve, telegram, slack and acp, so the section
// must not be wired into it — only the TUI adds it, from its own Run.
func TestNothingButTheTUIGetsTheHerdrSection(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "t16")

	for name, build := range map[string]func() *agent.Agent{
		"newInteractiveAgent (serve, telegram, slack, acp)": func() *agent.Agent { return newInteractiveAgent(false) },
		"newAgent":             newAgent,
		"newOneShotAgent (-p)": newOneShotAgent,
		"newConfigAgent":       newConfigAgent,
	} {
		t.Run(name, func(t *testing.T) {
			a := build()
			defer a.Close()
			if got := promptOf(t, a); strings.Contains(got, herdrSectionMarker) {
				t.Errorf("%s carries the Herdr section", name)
			}
		})
	}
}

// A sub-agent (the Subagent tool) and every session a Fork/Resume reopens inherit the environment of
// the process, but never the agent-wide directives of a DIFFERENT agent; the parent's own sessions
// keep theirs across /resume and /fork, which are how the TUI changes session.
func TestTheSectionSurvivesResumeAndFork(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "t16")
	a := (&tuiCmd{}).newTUIAgent()
	defer a.Close()

	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer models.Close()
	herdrFakeSeq++
	name := fmt.Sprintf("cli-herdr-fake-%d", herdrFakeSeq)
	if err := agent.NewOpenAIProvider(name, models.URL); err != nil {
		t.Fatal(err)
	}
	defer agent.UnregisterCustomProvider(name)
	cwd, _ := os.Getwd()
	sess, err := a.NewSession(cwd, name+"/m")
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := a.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumed.SystemPrompt(), herdrSectionMarker) {
		t.Error("a resumed session lost the Herdr section")
	}
	forked, err := a.ForkSession(id)
	if err != nil {
		t.Fatal(err)
	}
	defer forked.Close()
	if !strings.Contains(forked.SystemPrompt(), herdrSectionMarker) {
		t.Error("a forked session lost the Herdr section")
	}
	resumed.Close()
}

// tuiDirectives is the one place the decision lives.
func TestTuiDirectivesFollowTheEnvironment(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "")
	if got := tuiDirectives(); len(got) != 0 {
		t.Errorf("outside Herdr: %q", got)
	}
	t.Setenv("HC_TERMINAL_ID", "t5")
	got := tuiDirectives()
	if len(got) != 1 || !strings.HasPrefix(got[0], "## Herdr") {
		t.Errorf("inside Herdr: %q", got)
	}
}

// Run must get its agent from newTUIAgent, or the tests above would be checking an agent the real
// command never builds.
func TestRunBuildsItsAgentThroughNewTUIAgent(t *testing.T) {
	src, err := os.ReadFile("kong_run_tui.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (c *tuiCmd) Run() error {")
	j := strings.Index(body, "// newTUIAgent builds")
	if i < 0 || j < i {
		t.Fatal("cannot locate tuiCmd.Run in kong_run_tui.go")
	}
	run := body[i:j]
	if !strings.Contains(run, "c.newTUIAgent()") {
		t.Error("tuiCmd.Run does not build its agent with newTUIAgent")
	}
	if strings.Contains(run, "newInteractiveAgent(") {
		t.Error("tuiCmd.Run builds its agent with newInteractiveAgent directly, bypassing the TUI's directives")
	}
}
