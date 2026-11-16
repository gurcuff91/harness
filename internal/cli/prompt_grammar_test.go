package cli

import (
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/gurcuff91/harness/agent"
)

// parsed is what parseArgs made of a command line: the command Kong selected and the pieces of the
// root TUI command that matter here. The grammar is a package global (CLI), which Kong resets on
// every parse, so each case builds a fresh parser exactly as Main does (newParser).
type parsed struct {
	cmd      string // ctx.Command(): "tui", "tui <prompt>", "serve <addr>", "sessions"…
	prompt   string // the positional prompt, as the TUI will receive it
	model    string
	thinking string
	resume   string
	oneShot  string // -p
	err      string // "" when it parsed
}

func parse(args ...string) parsed {
	p, err := newParser()
	if err != nil {
		return parsed{err: "BUILD: " + err.Error()}
	}
	ctx, err := parseArgs(p, args)
	if err != nil {
		return parsed{err: err.Error()}
	}
	return parsed{
		cmd: ctx.Command(), prompt: CLI.TUI.initialPrompt(), model: CLI.TUI.Model,
		thinking: CLI.TUI.Thinking, resume: CLI.TUI.Resume, oneShot: CLI.TUI.Prompt,
	}
}

func TestAPositionalPromptOpensTheTUIWithIt(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want parsed
	}{
		"no arguments: the TUI, no prompt":                       {nil, parsed{cmd: "tui"}},
		"one quoted prompt":                                      {[]string{"fix the login bug"}, parsed{cmd: "tui <prompt>", prompt: "fix the login bug"}},
		"words are joined with one space":                        {[]string{"fix", "the", "login", "bug"}, parsed{cmd: "tui <prompt>", prompt: "fix the login bug"}},
		"flags before the prompt":                                {[]string{"--model", "anthropic/claude-x", "review src/x.rs"}, parsed{cmd: "tui <prompt>", prompt: "review src/x.rs", model: "anthropic/claude-x"}},
		"flags after the prompt":                                 {[]string{"review src/x.rs", "--model", "anthropic/claude-x"}, parsed{cmd: "tui <prompt>", prompt: "review src/x.rs", model: "anthropic/claude-x"}},
		"--thinking applies":                                     {[]string{"--thinking", "high", "x"}, parsed{cmd: "tui <prompt>", prompt: "x", thinking: "high"}},
		"--resume plus a prompt":                                 {[]string{"--resume", "abc123", "next question"}, parsed{cmd: "tui <prompt>", prompt: "next question", resume: "abc123"}},
		"--resume alone has no prompt":                           {[]string{"--resume", "abc123"}, parsed{cmd: "tui", resume: "abc123"}},
		"-p alone is unchanged (one-shot)":                       {[]string{"-p", "x"}, parsed{cmd: "tui", oneShot: "x"}},
		"--scheduler plus a prompt":                              {[]string{"--scheduler", "go"}, parsed{cmd: "tui <prompt>", prompt: "go"}},
		"an empty prompt is no prompt":                           {[]string{""}, parsed{cmd: "tui <prompt>", prompt: ""}},
		"a blank prompt is no prompt":                            {[]string{"   \n\t "}, parsed{cmd: "tui <prompt>", prompt: ""}},
		"blank words are no prompt":                              {[]string{"", " ", ""}, parsed{cmd: "tui <prompt>", prompt: ""}},
		"newlines and non-ASCII are intact":                      {[]string{"línea 1\nlínea 2 ñandú 日本語 🚀"}, parsed{cmd: "tui <prompt>", prompt: "línea 1\nlínea 2 ñandú 日本語 🚀"}},
		"a prompt that starts with / is text":                    {[]string{"/quit"}, parsed{cmd: "tui <prompt>", prompt: "/quit"}},
		"a prompt that starts with ! is text":                    {[]string{"!rm -rf /tmp/x"}, parsed{cmd: "tui <prompt>", prompt: "!rm -rf /tmp/x"}},
		"a prompt with a dash inside":                            {[]string{"explain a - b"}, parsed{cmd: "tui <prompt>", prompt: "explain a - b"}},
		"surrounding space is trimmed":                           {[]string{"  hello  "}, parsed{cmd: "tui <prompt>", prompt: "hello"}},
		"a word that merely contains a command name":             {[]string{"servers"}, parsed{cmd: "tui <prompt>", prompt: "servers"}},
		"a prompt that mentions a command":                       {[]string{"fix", "serve", ":8080"}, parsed{cmd: "tui <prompt>", prompt: "fix serve :8080"}},
		"a prompt that STARTS like a command but has more words": {[]string{"hello", "sessions"}, parsed{cmd: "tui <prompt>", prompt: "hello sessions"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := parse(c.args...); got != c.want {
				t.Errorf("harness %q:\n got  %+v\n want %+v", c.args, got, c.want)
			}
		})
	}
}

// A named subcommand WINS over a prompt. Each sibling is a regression: the positional was added to
// the root's default command, and none of these may become a prompt.
func TestEverySubcommandStillDispatchesToItself(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		cmd  string
	}{
		"sessions":             {[]string{"sessions"}, "sessions"},
		"sessions --all":       {[]string{"sessions", "--all"}, "sessions"},
		"providers":            {[]string{"providers"}, "providers"},
		"serve":                {[]string{"serve", ":8080"}, "serve <addr>"},
		"serve --scheduler":    {[]string{"serve", ":8080", "--scheduler"}, "serve <addr>"},
		"acp":                  {[]string{"acp"}, "acp"},
		"connect":              {[]string{"connect", "anthropic"}, "connect <name>"},
		"disconnect":           {[]string{"disconnect", "anthropic"}, "disconnect <name>"},
		"delete":               {[]string{"delete", "abc"}, "delete <session-id>"},
		"settings":             {[]string{"settings"}, "settings show"},
		"mcp":                  {[]string{"mcp"}, "mcp list"},
		"mcp add":              {[]string{"mcp", "add", "x", "--url", "http://x/mcp"}, "mcp add <name>"},
		"provider":             {[]string{"provider"}, "provider list"},
		"memo":                 {[]string{"memo"}, "memo"},
		"memo with a query":    {[]string{"memo", "needle"}, "memo <query>"},
		"schedules":            {[]string{"schedules"}, "schedules"},
		"telegram":             {[]string{"telegram"}, "telegram run"},
		"telegram pair":        {[]string{"telegram", "pair", "123"}, "telegram pair <chat-id>"},
		"slack":                {[]string{"slack"}, "slack run"},
		"slack login":          {[]string{"slack", "login"}, "slack login"},
		"a flag before a name": {[]string{"--model", "a/b", "sessions"}, "sessions"},
	} {
		t.Run(name, func(t *testing.T) {
			got := parse(c.args...)
			if got.err != "" {
				// some of these need arguments the test does not care about: report only a parse
				// error that is NOT about the command's own arguments
				if !strings.Contains(got.err, "expected") {
					t.Fatalf("harness %q: %s", c.args, got.err)
				}
			}
			if got.cmd != c.cmd && got.err == "" {
				t.Errorf("harness %q selected %q, want %q — it must not become a prompt", c.args, got.cmd, c.cmd)
			}
			if got.prompt != "" {
				t.Errorf("harness %q produced the prompt %q", c.args, got.prompt)
			}
		})
	}
}

// The ambiguity the story asks to settle: a prompt that IS a command name. The subcommand wins, and
// the prompt goes after --.
func TestAPromptThatIsACommandNameGoesAfterTheDoubleDash(t *testing.T) {
	// without --, the command wins
	for _, name := range []string{"sessions", "providers", "acp", "schedules", "memo"} {
		if got := parse(name); got.cmd == "tui" || got.cmd == "tui <prompt>" {
			t.Errorf("harness %s became %q: the subcommand must win over a prompt", name, got.cmd)
		}
	}
	// with --, it is the prompt
	for name, c := range map[string]struct {
		args   []string
		prompt string
		model  string
	}{
		"-- serve":                  {[]string{"--", "serve"}, "serve", ""},
		"-- sessions":               {[]string{"--", "sessions"}, "sessions", ""},
		"-- acp":                    {[]string{"--", "acp"}, "acp", ""},
		"-- providers":              {[]string{"--", "providers"}, "providers", ""},
		"-- several words":          {[]string{"--", "serve", "the", "site"}, "serve the site", ""},
		"-- starting with a dash":   {[]string{"--", "-p"}, "-p", ""},
		"-- a flag-looking word":    {[]string{"--", "--model", "x"}, "--model x", ""},
		"flags, then -- and a name": {[]string{"--model", "a/b", "--", "serve"}, "serve", "a/b"},
		"words, then -- and a name": {[]string{"explain", "--", "serve"}, "explain serve", ""},
		"-- after a --resume":       {[]string{"--resume", "ID", "--", "sessions"}, "sessions", ""},
		"-- alone is no prompt":     {[]string{"--"}, "", ""},
		"a second -- is text":       {[]string{"--", "--", "serve"}, "-- serve", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := parse(c.args...)
			if got.err != "" || (got.cmd != "tui" && got.cmd != "tui <prompt>") {
				t.Fatalf("harness %q: cmd=%q err=%q, want the TUI", c.args, got.cmd, got.err)
			}
			if got.prompt != c.prompt || got.model != c.model {
				t.Errorf("harness %q: prompt=%q model=%q, want %q / %q", c.args, got.prompt, got.model, c.prompt, c.model)
			}
		})
	}
	// -- after a real subcommand changes nothing: it is that command's own business, as before
	for _, args := range [][]string{{"sessions", "--", "x"}, {"serve", ":80", "--", "x"}} {
		if got := parse(args...); got.err == "" && (got.cmd == "tui" || got.cmd == "tui <prompt>") {
			t.Errorf("harness %q turned a real subcommand into the TUI", args)
		}
	}
}

func TestAPromptCannotBeCombinedWithTheOneShotFlag(t *testing.T) {
	const want = "use either -p (one-shot) or a positional prompt (interactive)"
	for name, args := range map[string][]string{
		"-p then a word":      {"-p", "x", "y"},
		"a word then -p":      {"y", "-p", "x"},
		"--prompt long form":  {"--prompt", "x", "y"},
		"-p with words":       {"-p", "x", "fix", "the", "bug"},
		"-p with -- and text": {"-p", "x", "--", "y"},
		"-p with a flag too":  {"--model", "a/b", "-p", "x", "y"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := parse(args...); got.err != want {
				t.Errorf("harness %q: err = %q, want %q", args, got.err, want)
			}
		})
	}
	// ...but a BLANK positional is no prompt, so it is not a conflict
	if got := parse("-p", "x", ""); got.err != "" || got.oneShot != "x" {
		t.Errorf("-p with an empty positional: %+v, want the one-shot run unchanged", got)
	}
	if got := parse("-p", "x", "  "); got.err != "" {
		t.Errorf("-p with a blank positional: err=%q", got.err)
	}
}

func TestUsageErrorsAreStillUsageErrors(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		part string
	}{
		"unknown flag":                 {[]string{"--nope"}, "unknown flag"},
		"unknown flag before a prompt": {[]string{"--nope", "go"}, "unknown flag"},
		"a bad enum":                   {[]string{"--thinking", "bogus", "go"}, "thinking"},
		"a flag missing its value":     {[]string{"--model"}, "model"},
		"serve without its address":    {[]string{"serve"}, "<addr>"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := parse(c.args...); got.err == "" || !strings.Contains(got.err, c.part) {
				t.Errorf("harness %q: err=%q, want it to mention %q", c.args, got.err, c.part)
			}
		})
	}
}

// What the grammar does to a typo — recorded, because it is the cost the story names: a mistyped
// command is now a prompt instead of an error.
func TestATypoedCommandNameIsNowAPrompt(t *testing.T) {
	got := parse("sesions")
	if got.cmd != "tui <prompt>" || got.prompt != "sesions" {
		t.Errorf("harness sesions: %+v — documented behaviour: it becomes the prompt \"sesions\"", got)
	}
}

// The parser must hold no state between parses: a prompt from one line must not leak into the next.
func TestNoStateLeaksBetweenParses(t *testing.T) {
	parse("--model", "a/b", "first prompt")
	if got := parse(); got.prompt != "" || got.model != "" || got.cmd != "tui" {
		t.Errorf("a bare parse after one with a prompt and a flag: %+v", got)
	}
	parse("--", "serve")
	if got := parse("sessions"); got.cmd != "sessions" || got.prompt != "" {
		t.Errorf("a command after a `--` prompt: %+v", got)
	}
}

func TestInitialPromptJoinsAndTrims(t *testing.T) {
	for in, want := range map[string]string{
		"a|b|c":      "a b c",
		"  a | b ":   "a   b", // inner spacing is the caller's; only the ends are trimmed
		"":           "",
		"|":          "",
		" |\n|\t":    "",
		"x\ny":       "x\ny",
		"multi word": "multi word",
	} {
		c := tuiCmd{Initial: strings.Split(in, "|")}
		if got := c.initialPrompt(); got != want {
			t.Errorf("initialPrompt(%q) = %q, want %q", in, got, want)
		}
	}
	if got := (&tuiCmd{}).initialPrompt(); got != "" {
		t.Errorf("no words: %q", got)
	}
}

// The wiring from the parsed command line to the TUI: what newTUI hands over is exactly the prompt
// the grammar parsed. (Run itself needs a terminal; newTUI is Run minus that.)
func TestTheParsedPromptReachesTheTUI(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"a quoted prompt":             {[]string{"fix the login bug"}, "fix the login bug"},
		"words joined with one space": {[]string{"fix", "the", "login", "bug"}, "fix the login bug"},
		"after --":                    {[]string{"--", "serve"}, "serve"},
		"newlines and non-ASCII":      {[]string{"línea 1\nlínea 2 ñ 日本語 🚀"}, "línea 1\nlínea 2 ñ 日本語 🚀"},
		"a leading /":                 {[]string{"/quit"}, "/quit"},
		"with a flag":                 {[]string{"--model", "a/b", "go"}, "go"},
		"blank":                       {[]string{"  "}, ""},
		"none":                        {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := newParser()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseArgs(p, c.args); err != nil {
				t.Fatal(err)
			}
			a := agent.New(agent.AgentOptions{})
			defer a.Close()
			if got := CLI.TUI.newTUI(a).InitialPrompt(); got != c.want {
				t.Errorf("harness %q hands the TUI %q, want %q", c.args, got, c.want)
			}
		})
	}
}

// helpOf renders `harness <args> --help` the way the binary does and returns what it prints.
func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	var out strings.Builder
	p, err := newParser(kong.Writers(&out, &out), kong.Exit(func(int) {}))
	if err != nil {
		t.Fatal(err)
	}
	p.Parse(append(args, "--help")) //nolint:errcheck — help prints and "exits"
	return out.String()
}

// The new argument is documented where a user looks: the root's help. And only there: it must not
// leak into the help of the other commands (the same trap the default command's flags avoid).
func TestTheHelpDocumentsThePromptOnlyOnTheRoot(t *testing.T) {
	root := helpOf(t)
	for _, want := range []string{"Usage: harness [<prompt> ...] [flags]", "First message", "interactive TUI", "after --", "-p for a one-shot"} {
		if !strings.Contains(root, want) {
			t.Errorf("the root help lacks %q:\n%s", want, root)
		}
	}
	for _, args := range [][]string{{"serve"}, {"telegram"}, {"sessions"}, {"acp"}, {"mcp"}} {
		if h := helpOf(t, args...); strings.Contains(h, "<prompt>") || strings.Contains(h, "First message") {
			t.Errorf("harness %v --help mentions the root's prompt:\n%s", args, h)
		}
	}
}
