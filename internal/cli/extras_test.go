package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/server"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// ── the environment variable ─────────────────────────────────────────────────────────────────────

// An empty or unset variable is "off" and never an error: `export X=`, a .env line with nothing after
// the `=` and an unset `${X}` are everyday, and Kong's own bool-from-env refuses them ("bool value must
// be true, 1, yes, false, 0 or no but got \"\"", measured on 1.16) and even fails a CORRECT --flag.
func TestAnEmptyVariableIsOffAndNeverAnError(t *testing.T) {
	for _, v := range []string{"", " ", "\t", "  \n "} {
		got, err := envSwitch("HARNESS_WITH_COLLEAGUES", envOf(map[string]string{"HARNESS_WITH_COLLEAGUES": v}))
		if err != nil || got {
			t.Errorf("%q: got (%v, %v), want (false, nil)", v, got, err)
		}
	}
	if got, err := envSwitch("HARNESS_WITH_COLLEAGUES", envOf(nil)); err != nil || got {
		t.Errorf("unset: got (%v, %v), want (false, nil)", got, err)
	}
}

func TestTheVariableAcceptsTheUsualSpellings(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "True", "yes", "YES", " 1 ", "\ttrue\n"} {
		if got, err := envSwitch("X", envOf(map[string]string{"X": v})); err != nil || !got {
			t.Errorf("%q: got (%v, %v), want (true, nil)", v, got, err)
		}
	}
	for _, v := range []string{"0", "false", "FALSE", "no", "No", " 0 "} {
		if got, err := envSwitch("X", envOf(map[string]string{"X": v})); err != nil || got {
			t.Errorf("%q: got (%v, %v), want (false, nil)", v, got, err)
		}
	}
}

// Anything else is an error that says which variable and what it may be: a typo in a deployment must
// not silently leave a feature on or off.
func TestAnUnknownValueIsRefusedAndNamesTheVariable(t *testing.T) {
	for _, v := range []string{"maybe", "2", "on", "off", "y", "enabled", "1 1", "true,false"} {
		got, err := envSwitch("HARNESS_WITH_SCHEDULER", envOf(map[string]string{"HARNESS_WITH_SCHEDULER": v}))
		if err == nil {
			t.Errorf("%q was accepted (as %v)", v, got)
			continue
		}
		for _, want := range []string{"HARNESS_WITH_SCHEDULER", strings.TrimSpace(v), "1, true, yes, 0, false or no"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error %q does not mention %q", v, err, want)
			}
		}
	}
}

// ── flags and variables together ────────────────────────────────────────────────────────────────

// Either one is enough, and a flag can only turn something on: there is no --without, so a flag never
// switches off what the environment switched on, and a false variable never switches off a flag.
func TestEitherTheFlagOrTheVariableTurnsAnExtraOn(t *testing.T) {
	type in struct {
		flags withFlags
		env   map[string]string
	}
	for name, c := range map[string]struct {
		in   in
		want extras
	}{
		"nothing":                        {in{}, extras{}},
		"colleagues flag":                {in{flags: withFlags{WithColleagues: true}}, extras{colleagues: true}},
		"scheduler flag":                 {in{flags: withFlags{WithScheduler: true}}, extras{scheduler: true}},
		"colleagues variable":            {in{env: map[string]string{"HARNESS_WITH_COLLEAGUES": "1"}}, extras{colleagues: true}},
		"scheduler variable":             {in{env: map[string]string{"HARNESS_WITH_SCHEDULER": "yes"}}, extras{scheduler: true}},
		"both flags":                     {in{flags: withFlags{true, true}}, extras{true, true}},
		"both variables":                 {in{env: map[string]string{"HARNESS_WITH_COLLEAGUES": "1", "HARNESS_WITH_SCHEDULER": "1"}}, extras{true, true}},
		"a flag and the OTHER variable":  {in{flags: withFlags{WithColleagues: true}, env: map[string]string{"HARNESS_WITH_SCHEDULER": "1"}}, extras{true, true}},
		"a flag beats a false variable":  {in{flags: withFlags{WithColleagues: true}, env: map[string]string{"HARNESS_WITH_COLLEAGUES": "0"}}, extras{colleagues: true}},
		"a false variable alone is off":  {in{env: map[string]string{"HARNESS_WITH_COLLEAGUES": "false", "HARNESS_WITH_SCHEDULER": "no"}}, extras{}},
		"an empty variable is off":       {in{env: map[string]string{"HARNESS_WITH_COLLEAGUES": "", "HARNESS_WITH_SCHEDULER": ""}}, extras{}},
		"an empty variable and the flag": {in{flags: withFlags{WithScheduler: true}, env: map[string]string{"HARNESS_WITH_SCHEDULER": ""}}, extras{scheduler: true}},
	} {
		got, err := extrasFrom(c.in.flags, envOf(c.in.env))
		if err != nil || got != c.want {
			t.Errorf("%s: got (%+v, %v), want %+v", name, got, err, c.want)
		}
	}
}

// The two variables are independent: one cannot switch the other.
func TestTheTwoVariablesAreIndependent(t *testing.T) {
	got, _ := extrasFrom(withFlags{}, envOf(map[string]string{"HARNESS_WITH_COLLEAGUES": "1"}))
	if got.scheduler || !got.colleagues {
		t.Errorf("HARNESS_WITH_COLLEAGUES alone gave %+v", got)
	}
	got, _ = extrasFrom(withFlags{}, envOf(map[string]string{"HARNESS_WITH_SCHEDULER": "1"}))
	if got.colleagues || !got.scheduler {
		t.Errorf("HARNESS_WITH_SCHEDULER alone gave %+v", got)
	}
}

// A bad value is an error even when the flag is given: the typo is reported, not hidden by the flag.
func TestABadVariableIsReportedEvenWithTheFlag(t *testing.T) {
	if _, err := extrasFrom(withFlags{WithColleagues: true}, envOf(map[string]string{"HARNESS_WITH_COLLEAGUES": "maybe"})); err == nil {
		t.Error("a malformed HARNESS_WITH_COLLEAGUES was ignored because --with-colleagues was given")
	}
	// ... and a malformed OTHER variable fails too
	if _, err := extrasFrom(withFlags{WithColleagues: true}, envOf(map[string]string{"HARNESS_WITH_SCHEDULER": "maybe"})); err == nil {
		t.Error("a malformed HARNESS_WITH_SCHEDULER was ignored")
	}
}

// The real environment.
func TestExtrasOfReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("HARNESS_WITH_COLLEAGUES", "1")
	t.Setenv("HARNESS_WITH_SCHEDULER", "")
	got, err := extrasOf(withFlags{})
	if err != nil || got != (extras{colleagues: true}) {
		t.Errorf("got (%+v, %v)", got, err)
	}
	t.Setenv("HARNESS_WITH_SCHEDULER", "banana")
	if _, err := extrasOf(withFlags{}); err == nil {
		t.Error("a malformed real variable was accepted")
	}
}

// ── the grammar ─────────────────────────────────────────────────────────────────────────────────

// The same two flags, declared once, on every command that builds an agent; nowhere else; and the old
// name is gone (no alias, by decision: a flag rename, so whoever used --scheduler passes the new one).
func TestTheWithFlagsExistOnTheAgentCommandsAndNowhereElse(t *testing.T) {
	parse := func(args ...string) error {
		p, err := newParser()
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Parse(args)
		return err
	}
	for _, c := range [][]string{
		{"--with-colleagues"}, {"--with-scheduler"}, {"--with-colleagues", "--with-scheduler", "hello"},
		{"serve", ":8080", "--with-colleagues"}, {"serve", ":8080", "--with-scheduler"},
		{"slack", "--with-colleagues"}, {"slack", "--with-scheduler"},
		{"telegram", "--with-colleagues"}, {"telegram", "--with-scheduler"},
	} {
		if err := parse(c...); err != nil {
			t.Errorf("harness %v: %v", c, err)
		}
	}
	for _, c := range [][]string{
		{"--scheduler"}, {"serve", ":8080", "--scheduler"}, {"slack", "--scheduler"}, {"telegram", "--scheduler"}, // the OLD name: gone, no alias
		{"--with-web-search"},        // WebSearch is always on: there is no switch for it
		{"acp", "--with-colleagues"}, // ACP is started by an editor and takes no flags
		{"acp", "--with-scheduler"},
		{"sessions", "--with-colleagues"}, // config commands do no agent work
		{"providers", "--with-scheduler"},
		{"--with-colleagues=maybe"}, // a flag is a plain switch, not a value
	} {
		err := parse(c...)
		if err == nil {
			t.Errorf("harness %v parsed, want it refused", c)
		} else if !strings.Contains(err.Error(), "unknown flag") && !strings.Contains(err.Error(), "unexpected") && !strings.Contains(err.Error(), "bool") {
			t.Errorf("harness %v: err = %v", c, err)
		}
	}
}

// The flags land in the command that Run reads, for every command that has them. A fresh parser per
// case: Kong fills the package-level CLI, so one that has already parsed --with-colleagues would still
// hold it and a case that did NOT pass the flag could not tell.
func TestTheFlagsReachTheCommandsRun(t *testing.T) {
	for _, c := range []struct {
		args []string
		get  func() withFlags
	}{
		{[]string{"--with-colleagues", "--with-scheduler"}, func() withFlags { return CLI.TUI.withFlags }},
		{[]string{"--with-colleagues"}, func() withFlags { return CLI.TUI.withFlags }},
		{[]string{"--with-scheduler"}, func() withFlags { return CLI.TUI.withFlags }},
		{[]string{"hello"}, func() withFlags { return CLI.TUI.withFlags }},
		{[]string{"serve", ":1", "--with-colleagues"}, func() withFlags { return CLI.Serve.withFlags }},
		{[]string{"serve", ":1"}, func() withFlags { return CLI.Serve.withFlags }},
		{[]string{"slack", "--with-scheduler"}, func() withFlags { return CLI.Slack.Run.withFlags }},
		{[]string{"slack"}, func() withFlags { return CLI.Slack.Run.withFlags }},
		{[]string{"telegram", "--with-colleagues", "--with-scheduler"}, func() withFlags { return CLI.Telegram.Run.withFlags }},
		{[]string{"telegram"}, func() withFlags { return CLI.Telegram.Run.withFlags }},
	} {
		p, err := newParser()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseArgs(p, c.args); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		got := c.get()
		wantC, wantS := false, false
		for _, a := range c.args {
			wantC = wantC || a == "--with-colleagues"
			wantS = wantS || a == "--with-scheduler"
		}
		if got.WithColleagues != wantC || got.WithScheduler != wantS {
			t.Errorf("%v: command got %+v, want colleagues=%v scheduler=%v", c.args, got, wantC, wantS)
		}
	}
}

// ── what each command's agent really has ────────────────────────────────────────────────────────

// modelsFake serves the model list the fake provider needs, once per test.
func fakeProviderModel(t *testing.T) string {
	t.Helper()
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[{"id":"m"}]}`)) }))
	t.Cleanup(models.Close)
	herdrFakeSeq++
	name := fmt.Sprintf("cli-extras-fake-%d", herdrFakeSeq)
	if err := agent.NewOpenAIProvider(name, models.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(name) })
	return name + "/m"
}

// seenByTheModel opens a real session on a and returns the tool names the model is offered and the
// system prompt it receives. The decision seen from the model's side, not from the options passed.
func seenByTheModel(t *testing.T, a *agent.Agent) (names map[string]bool, prompt string) {
	t.Helper()
	sess, err := a.NewSession(t.TempDir(), fakeProviderModel(t))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	names = map[string]bool{}
	for _, d := range sess.Tools() {
		names[d.Name] = true
	}
	return names, sess.SystemPrompt()
}

const (
	colleagueSection  = "## Colleagues"
	schedulingSection = "## Scheduling"
)

// The table of the plan. For each way of building an agent, with and without the extras: which tools the
// model has and which sections its system prompt carries.
func TestWhatEachAgentIsGiven(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "")
	base := []string{"Bash", "Read", "Write", "Edit", "Fetch", "SessionInfo", "Subagent"}
	colleagueTools := []string{"ColleagueList", "ColleagueAsk"}
	scheduleTools := []string{"Schedule", "ScheduleList", "ScheduleDelete"}

	type want struct{ colleagues, scheduler, webSearch bool }
	for name, c := range map[string]struct {
		build func() *agent.Agent
		want  want
	}{
		"interactive, no extras":              {func() *agent.Agent { return newInteractiveAgent(extras{}) }, want{false, false, true}},
		"interactive, colleagues":             {func() *agent.Agent { return newInteractiveAgent(extras{colleagues: true}) }, want{true, false, true}},
		"interactive, scheduler":              {func() *agent.Agent { return newInteractiveAgent(extras{scheduler: true}) }, want{false, true, true}},
		"interactive, both":                   {func() *agent.Agent { return newInteractiveAgent(extras{true, true}) }, want{true, true, true}},
		"the TUI's agent, no extras":          {func() *agent.Agent { return (&tuiCmd{}).newTUIAgent(extras{}) }, want{false, false, true}},
		"the TUI's agent, both":               {func() *agent.Agent { return (&tuiCmd{}).newTUIAgent(extras{true, true}) }, want{true, true, true}},
		"-p (one-shot)":                       {newOneShotAgent, want{false, false, true}},
		"newAgent (mcp, memo)":                {newAgent, want{false, false, false}},
		"newConfigAgent (settings, sessions)": {newConfigAgent, want{false, false, false}},
	} {
		t.Run(name, func(t *testing.T) {
			a := c.build()
			defer a.Close()
			tools, prompt := seenByTheModel(t, a)
			for _, n := range base {
				if !tools[n] {
					t.Errorf("lost the base tool %s", n)
				}
			}
			for _, n := range colleagueTools {
				if tools[n] != c.want.colleagues {
					t.Errorf("tool %s present=%v, want %v", n, tools[n], c.want.colleagues)
				}
			}
			for _, n := range scheduleTools {
				if tools[n] != c.want.scheduler {
					t.Errorf("tool %s present=%v, want %v", n, tools[n], c.want.scheduler)
				}
			}
			if tools["WebSearch"] != c.want.webSearch {
				t.Errorf("tool WebSearch present=%v, want %v", tools["WebSearch"], c.want.webSearch)
			}
			if got := strings.Contains(prompt, colleagueSection); got != c.want.colleagues {
				t.Errorf("the system prompt has the Colleagues section = %v, want %v", got, c.want.colleagues)
			}
			if got := strings.Contains(prompt, schedulingSection); got != c.want.scheduler {
				t.Errorf("the system prompt has the Scheduling section = %v, want %v", got, c.want.scheduler)
			}
		})
	}
}

// The reason for the whole change, in numbers: the light agent is much smaller than the one the TUI
// built before, in the prompt AND in the tool definitions that go with every request. Measured here, not
// asserted from memory, so a regression that puts colleagues back by default fails on size too.
func TestTheLightAgentIsMuchSmaller(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "")
	size := func(a *agent.Agent) (promptChars, toolBytes int) {
		sess, err := a.NewSession(t.TempDir(), fakeProviderModel(t))
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		for _, d := range sess.Tools() {
			b, _ := json.Marshal(d)
			toolBytes += len(b)
		}
		return len(sess.SystemPrompt()), toolBytes
	}
	light := newInteractiveAgent(extras{})
	defer light.Close()
	full := newInteractiveAgent(extras{true, true})
	defer full.Close()
	lp, lt := size(light)
	fp, ft := size(full)
	if lp >= fp || lt >= ft {
		t.Fatalf("light=(%d chars, %d bytes) is not smaller than full=(%d, %d)", lp, lt, fp, ft)
	}
	// colleagues alone are about 2 500 characters of prompt and 3 000 bytes of tool definitions
	if fp-lp < 2000 || ft-lt < 2500 {
		t.Errorf("the extras weigh only %d chars of prompt and %d bytes of tools; the claim is about 2 800 and 4 600", fp-lp, ft-lt)
	}
}

// A process without --with-colleagues is still an instance other agents can find and ask: the registry is
// written by the SERVER, not by the tools, so it only lacks the tools to ask THEM. This is the behaviour the
// owner expects, so it is tested for real: a server over an agent with NO colleague tools registers itself,
// shows up in the registry other agents read, and answers a prompt sent to it the way ColleagueAsk sends one.
func TestWithoutColleaguesAnInstanceIsStillRegisteredAndAnswers(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "")

	// a model that answers with a fixed sentence
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"pong from the light instance"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer models.Close()
	herdrFakeSeq++
	prov := fmt.Sprintf("cli-extras-answer-%d", herdrFakeSeq)
	if err := agent.NewOpenAIProvider(prov, models.URL); err != nil {
		t.Fatal(err)
	}
	defer agent.UnregisterCustomProvider(prov)

	a := newInteractiveAgent(extras{}) // NO colleagues, NO scheduler
	srv, err := startTransportServer(a, "tui")
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	defer srv.Close()

	// 1. it is in the registry (what GET /api/instances and ColleagueList read). Start serves on a
	// goroutine and the registration happens inside Serve, so it lands a moment AFTER Start returns:
	// wait for it with a deadline instead of reading once.
	var registered *server.InstanceInfo
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, info := range registeredInstances(t) {
			if info.URL == "http://"+srv.Addr() {
				info := info
				registered = &info
			}
		}
		if registered != nil {
			break
		}
	}
	if registered == nil {
		t.Fatalf("a server over an agent with no colleague tools is not in the registry (%s)", srv.Addr())
	}
	if registered.Transport != "tui" {
		t.Errorf("registered as transport %q, want tui", registered.Transport)
	}

	// 2. it answers a prompt sent over its API, which is how a colleague asks
	c := client.New(srv.Addr())
	sess, err := c.CreateSession(prov+"/m", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := c.Ask(sess.ID, "ping", 30*time.Second, client.WithOrigin("colleague"))
	if err != nil {
		t.Fatalf("the light instance did not answer a colleague's prompt: %v", err)
	}
	if !strings.Contains(reply, "pong from the light instance") {
		t.Errorf("reply = %q", reply)
	}

	// 3. and the model that answered did NOT have the tools to ask others
	tools := map[string]bool{}
	live, err := a.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range live.Tools() {
		tools[d.Name] = true
	}
	if tools["ColleagueList"] || tools["ColleagueAsk"] {
		t.Errorf("the light instance has colleague tools: %v", tools)
	}
}

// registeredInstances reads the registry the way the colleague tools and GET /api/instances do.
func registeredInstances(t *testing.T) map[string]server.InstanceInfo {
	t.Helper()
	m, err := server.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ── the commands' own wiring: Run's agent, with the flags and the environment ───────────────────

// toolsOf returns the tool names a real session on a offers the model, and the system prompt.
func toolsOf(t *testing.T, a *agent.Agent) (map[string]bool, string) { return seenByTheModel(t, a) }

func parseFor(t *testing.T, args ...string) {
	t.Helper()
	p, err := newParser()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseArgs(p, args); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
}

// The agent each long-running command really builds, from the command line it was given and the
// environment it was started in: colleague tools and the Colleagues section iff colleagues were asked
// for; Schedule tools and the Scheduling section iff the scheduler was. Every command, the three ways to
// ask (flag, variable, both) and the way to not ask.
func TestEveryCommandBuildsTheAgentItWasAskedFor(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "")

	type build func() (*agent.Agent, error)
	cmds := map[string]struct {
		args  []string // nil for ACP: it takes no flags
		build build
	}{
		"tui": {[]string{"hello"}, func() (*agent.Agent, error) {
			x, err := extrasOf(CLI.TUI.withFlags)
			if err != nil {
				return nil, err
			}
			return CLI.TUI.newTUIAgent(x), nil
		}},
		"serve":    {[]string{"serve", ":0"}, func() (*agent.Agent, error) { return CLI.Serve.newAgent() }},
		"slack":    {[]string{"slack"}, func() (*agent.Agent, error) { return CLI.Slack.Run.newAgent() }},
		"telegram": {[]string{"telegram"}, func() (*agent.Agent, error) { return CLI.Telegram.Run.newAgent() }},
		"acp":      {nil, func() (*agent.Agent, error) { return CLI.ACP.newAgent() }},
	}
	flag := func(extra string, args []string) []string {
		if args == nil || extra == "" {
			return args
		}
		if args[0] == "hello" { // the TUI: flags go before the prompt
			return append([]string{extra}, args...)
		}
		return append(append([]string{}, args...), extra)
	}

	for cmdName, c := range cmds {
		for caseName, k := range map[string]struct {
			flagArg string
			envName string
			col     bool
			sched   bool
		}{
			"nothing":                 {"", "", false, false},
			"--with-colleagues":       {"--with-colleagues", "", true, false},
			"--with-scheduler":        {"--with-scheduler", "", false, true},
			"HARNESS_WITH_COLLEAGUES": {"", "HARNESS_WITH_COLLEAGUES", true, false},
			"HARNESS_WITH_SCHEDULER":  {"", "HARNESS_WITH_SCHEDULER", false, true},
		} {
			// ACP has no flags: only the environment can ask
			if c.args == nil && k.flagArg != "" {
				continue
			}
			t.Run(cmdName+"/"+caseName, func(t *testing.T) {
				t.Setenv("HARNESS_WITH_COLLEAGUES", "")
				t.Setenv("HARNESS_WITH_SCHEDULER", "")
				if k.envName != "" {
					t.Setenv(k.envName, "1")
				}
				if c.args != nil {
					parseFor(t, flag(k.flagArg, c.args)...)
				}
				a, err := c.build()
				if err != nil {
					t.Fatal(err)
				}
				defer a.Close()
				tools, prompt := toolsOf(t, a)
				if got := tools["ColleagueList"] && tools["ColleagueAsk"]; got != k.col {
					t.Errorf("colleague tools present=%v, want %v", got, k.col)
				}
				if got := strings.Contains(prompt, colleagueSection); got != k.col {
					t.Errorf("Colleagues section present=%v, want %v", got, k.col)
				}
				if got := tools["Schedule"] && tools["ScheduleList"] && tools["ScheduleDelete"]; got != k.sched {
					t.Errorf("schedule tools present=%v, want %v", got, k.sched)
				}
				if got := strings.Contains(prompt, schedulingSection); got != k.sched {
					t.Errorf("Scheduling section present=%v, want %v", got, k.sched)
				}
				if !tools["WebSearch"] {
					t.Error("WebSearch is always on and was missing")
				}
			})
		}
	}
}

// A malformed variable stops the command with the message, for every command that has the extras, before
// anything is built.
func TestAMalformedVariableStopsEveryCommandThatHasTheExtras(t *testing.T) {
	isolate(t)
	t.Setenv("HARNESS_WITH_COLLEAGUES", "maybe")
	t.Setenv("HARNESS_WITH_SCHEDULER", "")
	parseFor(t, "serve", ":0")
	if a, err := CLI.Serve.newAgent(); err == nil {
		a.Close()
		t.Error("serve built an agent with HARNESS_WITH_COLLEAGUES=maybe")
	} else if !strings.Contains(err.Error(), "HARNESS_WITH_COLLEAGUES") {
		t.Errorf("serve: %v", err)
	}
	parseFor(t, "slack")
	if a, err := CLI.Slack.Run.newAgent(); err == nil {
		a.Close()
		t.Error("slack built an agent")
	}
	parseFor(t, "telegram")
	if a, err := CLI.Telegram.Run.newAgent(); err == nil {
		a.Close()
		t.Error("telegram built an agent")
	}
	if a, err := CLI.ACP.newAgent(); err == nil {
		a.Close()
		t.Error("acp built an agent")
	}
}

// The scheduler badge in the TUI's footer follows the SAME decision that built the agent: a flag, the
// variable, or neither. Colleagues never light it.
func TestTheTUIFooterBadgeFollowsTheScheduler(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "")
	for name, c := range map[string]struct {
		args []string
		env  map[string]string
		want bool
	}{
		"nothing":                    {[]string{"hi"}, nil, false},
		"--with-scheduler":           {[]string{"--with-scheduler", "hi"}, nil, true},
		"the variable":               {[]string{"hi"}, map[string]string{"HARNESS_WITH_SCHEDULER": "1"}, true},
		"colleagues do not light it": {[]string{"--with-colleagues", "hi"}, map[string]string{"HARNESS_WITH_COLLEAGUES": "1"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HARNESS_WITH_COLLEAGUES", "")
			t.Setenv("HARNESS_WITH_SCHEDULER", "")
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			parseFor(t, c.args...)
			x, err := extrasOf(CLI.TUI.withFlags)
			if err != nil {
				t.Fatal(err)
			}
			a := CLI.TUI.newTUIAgent(x)
			defer a.Close()
			if got := CLI.TUI.newTUI(a, x).SchedulerOn(); got != c.want {
				t.Errorf("the footer's scheduler badge is %v, want %v", got, c.want)
			}
		})
	}
}

// Run must read the extras ONCE and hand the same answer to the agent and to the TUI: two reads could
// disagree (a flag the agent saw and the footer did not), so the source is pinned.
func TestTheTUIRunDecidesTheExtrasOnce(t *testing.T) {
	src, err := os.ReadFile("kong_run_tui.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (c *tuiCmd) Run() error {")
	j := strings.Index(body, "// newTUIAgent builds")
	run := body[i:j]
	if n := strings.Count(run, "extrasOf("); n != 1 {
		t.Errorf("tuiCmd.Run calls extrasOf %d times, want exactly 1", n)
	}
	if !strings.Contains(run, "c.newTUIAgent(x)") || !strings.Contains(run, "c.newTUI(a, x)") {
		t.Error("tuiCmd.Run does not hand the same extras to the agent and to the TUI")
	}
}

// Each command's Run must get its agent from newAgent, or the tests above would be checking an agent the
// real command never builds (the same pin the TUI has, for the other four commands). Run itself needs a
// terminal, a network or an editor, so the source is what can be checked.
func TestEveryRunBuildsItsAgentThroughNewAgent(t *testing.T) {
	for file, run := range map[string]string{
		"kong_run_serve.go":    "func (c *serveCmd) Run() error {",
		"kong_run_slack.go":    "func (c *slackRunCmd) Run() error {",
		"kong_run_telegram.go": "func (c *telegramRunCmd) Run() error {",
		"kong_run_acp.go":      "func (c *acpCmd) Run() error {",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		i := strings.Index(body, run)
		if i < 0 {
			t.Fatalf("%s: cannot find %q", file, run)
		}
		end := strings.Index(body[i+1:], "\nfunc ")
		fn := body[i:]
		if end > 0 {
			fn = body[i : i+1+end]
		}
		if !strings.Contains(fn, "c.newAgent()") {
			t.Errorf("%s: Run does not build its agent with c.newAgent()", file)
		}
		if strings.Contains(fn, "newInteractiveAgent(") {
			t.Errorf("%s: Run builds its agent with newInteractiveAgent directly, bypassing the flags and the environment", file)
		}
	}
}
