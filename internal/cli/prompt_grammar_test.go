package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/version"
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
		"--with-scheduler plus a prompt":                         {[]string{"--with-scheduler", "go"}, parsed{cmd: "tui <prompt>", prompt: "go"}},
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
		"sessions":                {[]string{"sessions"}, "sessions"},
		"sessions --all":          {[]string{"sessions", "--all"}, "sessions"},
		"providers":               {[]string{"providers"}, "providers"},
		"serve":                   {[]string{"serve", ":8080"}, "serve <addr>"},
		"serve --with-scheduler":  {[]string{"serve", ":8080", "--with-scheduler"}, "serve <addr>"},
		"serve --with-colleagues": {[]string{"serve", ":8080", "--with-colleagues"}, "serve <addr>"},
		"acp":                     {[]string{"acp"}, "acp"},
		"connect":                 {[]string{"connect", "anthropic"}, "connect <name>"},
		"disconnect":              {[]string{"disconnect", "anthropic"}, "disconnect <name>"},
		"delete":                  {[]string{"delete", "abc"}, "delete <session-id>"},
		"settings":                {[]string{"settings"}, "settings show"},
		"mcp":                     {[]string{"mcp"}, "mcp list"},
		"mcp add":                 {[]string{"mcp", "add", "x", "--url", "http://x/mcp"}, "mcp add <name>"},
		"provider":                {[]string{"provider"}, "provider list"},
		"memo":                    {[]string{"memo"}, "memo"},
		"memo with a query":       {[]string{"memo", "needle"}, "memo <query>"},
		"schedules":               {[]string{"schedules"}, "schedules"},
		"telegram token":          {[]string{"telegram", "token", "--status"}, "telegram token"},
		"slack login":             {[]string{"slack", "login"}, "slack login"},
		"a flag before a name":    {[]string{"--model", "a/b", "sessions"}, "sessions"},
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
			if got := CLI.TUI.newTUI(a, extras{}).InitialPrompt(); got != c.want {
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

// ── a mistyped command is an error, not a message (Claude Code's rule) ──────

// wantUnknown asserts that the line fails as a mistyped command and returns the error.
func wantUnknown(t *testing.T, word, suggestion string, args ...string) {
	t.Helper()
	got := parse(args...)
	if got.err == "" {
		t.Errorf("harness %q parsed as %+v: want an unknown-command error (a typo must not cost a turn)", args, got)
		return
	}
	for _, want := range []string{`unknown command "` + word + `"`, "did you mean: harness " + suggestion, "harness -- ", "harness -p "} {
		if !strings.Contains(got.err, want) {
			t.Errorf("harness %q: error lacks %q:\n%s", args, want, got.err)
		}
	}
}

// The rule is Claude Code's, measured on its real binary: the FIRST word, within ONE edit of a
// command (insertion, deletion, substitution, or a swap of two neighbouring letters), ignoring case.
func TestAMistypedCommandIsAnErrorWithASuggestion(t *testing.T) {
	for _, c := range []struct{ word, want string }{
		{"sesions", "sessions"},   // a deleted letter
		{"sessoins", "sessions"},  // a swap of neighbours
		{"sessionss", "sessions"}, // an extra letter
		{"sessiins", "sessions"},  // a substituted letter
		{"providrs", "providers"},
		{"serv", "serve"},
		{"serve1", "serve"},
		{"acp1", "acp"},
		{"mcpp", "mcp"},
		{"mc", "mcp"},
		{"memoo", "memo"},
		{"setings", "settings"},
		{"conect", "connect"},
		{"delet", "delete"},
		{"SESIONS", "sessions"}, // case is ignored
		{"Sessoins", "sessions"},
		{"telegrm", "telegram"},
		{"slak", "slack"},
		{"serves", "serve"}, // one inserted letter: a typo of serve, not a message
		{"memos", "memo"},
		{"setting", "settings"}, // a singular is one deleted letter
		{"schedulez", "schedules"},
		{"hlep", "help"}, // the two words harness itself understands
		{"versoin", "version"},
	} {
		t.Run(c.word, func(t *testing.T) { wantUnknown(t, c.word, c.want, c.word) })
	}
}

// Only the FIRST word decides, and it decides even when more words follow (as in Claude Code:
// `claude doctr now` is an error, `claude fix the doctr bug` is a message). Flags before it change
// nothing.
func TestOnlyTheFirstWordIsCheckedAndItIsCheckedWithMoreWordsAndFlags(t *testing.T) {
	wantUnknown(t, "sesions", "sessions", "sesions", "now")
	wantUnknown(t, "sesions", "sessions", "sesions", "fix", "the", "bug")
	wantUnknown(t, "sesions", "sessions", "--model", "a/b", "sesions")
	wantUnknown(t, "mcpp", "mcp", "mcpp", "add", "x")
	for _, args := range [][]string{
		{"fix", "sesions"}, // a typo that is not the first word is just text
		{"fix", "the", "sesions", "bug"},
		{"run", "sesions", "now"},
	} {
		if got := parse(args...); got.err != "" || got.prompt == "" {
			t.Errorf("harness %q: %+v — only the first word is checked", args, got)
		}
	}
}

// Everything that is NOT one edit from a command is the message it probably is: a real one-word
// task, a path, a sentence. Two edits away is already a message (Claude: `doct`, `updt`, `agnt`).
func TestWordsThatAreNotCloseToACommandAreMessages(t *testing.T) {
	for _, w := range []string{
		"OK", "fix", "hello", "refactor", "src/x.rs", "README.md", "yes", "test", "build",
		"sesiones", // two edits from sessions (Spanish): a message
		"sesion",   // 'sessions' minus two letters
		"servers",  // two edits from serve (one inserted letter is "serves"; this adds another)
		"x", "ab", "zz",
	} {
		t.Run(w, func(t *testing.T) {
			if got := parse(w); got.err != "" || got.cmd != "tui <prompt>" || got.prompt != w {
				t.Errorf("harness %q: %+v, want it sent as a message", w, got)
			}
		})
	}
}

// The ways out, exactly the ones the error announces.
func TestTheErrorsWayOutsWork(t *testing.T) {
	if got := parse("--", "sesions"); got.err != "" || got.prompt != "sesions" || got.cmd != "tui" {
		t.Errorf("harness -- sesions: %+v, want the message \"sesions\"", got)
	}
	if got := parse("-p", "sesions"); got.err != "" || got.oneShot != "sesions" {
		t.Errorf("harness -p sesions: %+v, want the one-shot run", got)
	}
	if got := parse("--model", "a/b", "--", "serv"); got.err != "" || got.prompt != "serv" || got.model != "a/b" {
		t.Errorf("flags then `-- serv`: %+v", got)
	}
	// words after `--` are never checked, however many and whatever they look like
	if got := parse("--", "sesions", "mcpp", "serv"); got.err != "" || got.prompt != "sesions mcpp serv" {
		t.Errorf("harness -- sesions mcpp serv: %+v", got)
	}
}

// A command that is spelled right is the command, never an error and never a message.
func TestAnExactCommandNameIsTheCommand(t *testing.T) {
	for _, c := range []struct {
		args []string
		cmd  string
	}{
		{[]string{"sessions"}, "sessions"}, {[]string{"providers"}, "providers"}, {[]string{"acp"}, "acp"},
		{[]string{"schedules"}, "schedules"}, {[]string{"memo"}, "memo"},
	} {
		if got := parse(c.args...); got.err != "" || got.cmd != c.cmd {
			t.Errorf("harness %q: %+v, want %q", c.args, got, c.cmd)
		}
	}
	// and a spelling that is itself a command is not "close to another command": serve vs serves
	if got := parse("serve", ":0"); got.cmd != "serve <addr>" {
		t.Errorf("harness serve :0: %+v", got)
	}
}

// help and version are real now: they are what people type, and what used to cost a turn.
func TestHelpAndVersionAreCommands(t *testing.T) {
	// `harness help` → the root help; `harness help serve` → serve's help; `harness version` → the version
	root := helpOf(t)
	if !strings.Contains(root, "Usage: harness") {
		t.Fatalf("setup: no root help:\n%s", root)
	}
	var out strings.Builder
	p, err := newParser(kong.Writers(&out, &out), kong.Exit(func(int) {}))
	if err != nil {
		t.Fatal(err)
	}
	parseArgs(p, []string{"help"}) //nolint:errcheck
	if !strings.Contains(out.String(), "Usage: harness") {
		t.Errorf("harness help printed %q, want the root help", out.String())
	}
	out.Reset()
	p, _ = newParser(kong.Writers(&out, &out), kong.Exit(func(int) {}))
	parseArgs(p, []string{"help", "serve"}) //nolint:errcheck
	if !strings.Contains(out.String(), "Usage: harness serve") {
		t.Errorf("harness help serve printed %q, want serve's help", out.String())
	}
	out.Reset()
	p, _ = newParser(kong.Writers(&out, &out), kong.Exit(func(int) {}))
	parseArgs(p, []string{"version"}) //nolint:errcheck
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Errorf("harness version printed %q, want %q", got, version.Version)
	}
	// ...but `help` followed by words is a message, not a request for help
	if got := parse("help", "me", "fix", "this"); got.err != "" || got.prompt != "help me fix this" {
		t.Errorf("harness help me fix this: %+v, want the message", got)
	}
	// and `help` before something that is not a command is a message too
	if got := parse("help", "wanted"); got.err != "" || got.prompt != "help wanted" {
		t.Errorf("harness help wanted: %+v", got)
	}
}

func TestVersionFlags(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		var out strings.Builder
		p, err := newParser(kong.Writers(&out, &out), kong.Exit(func(int) {}))
		if err != nil {
			t.Fatal(err)
		}
		parseArgs(p, []string{flag}) //nolint:errcheck
		if got := strings.TrimSpace(out.String()); got != version.Version {
			t.Errorf("harness %s printed %q, want %q", flag, got, version.Version)
		}
	}
}

// editDistance is Claude Code's metric: one edit = insert, delete, substitute, or swap two
// neighbouring letters.
func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"a", "", 1}, {"", "ab", 2}, {"abc", "abc", 0},
		{"sesions", "sessions", 1}, {"sessoins", "sessions", 1}, // a swap is ONE edit
		{"udpate", "update", 1}, {"upadte", "update", 1}, // each is ONE swap of neighbours
		{"upadtte", "update", 2},
		{"doct", "doctor", 2}, {"docters", "doctor", 2}, {"ocdtor", "doctor", 2},
		{"kitten", "sitting", 3}, {"ab", "ba", 1}, {"abc", "bca", 2},
		{"ñandú", "nandu", 2}, {"日本", "日本語", 1}, // runes, not bytes
	} {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := editDistance(c.b, c.a); got != c.want {
			t.Errorf("editDistance(%q,%q) = %d, want %d (symmetric)", c.b, c.a, got, c.want)
		}
	}
}

// What `--help` and the typo check must agree on: the commands a user can see are exactly the ones
// a typo is compared against (a hidden command must not be suggested).
func TestSuggestionsComeFromTheVisibleCommandsOnly(t *testing.T) {
	p, err := newParser()
	if err != nil {
		t.Fatal(err)
	}
	names := commandNames(p)
	for _, want := range []string{"serve", "sessions", "providers", "acp", "telegram", "slack", "connect", "mcp", "memo", "settings", "schedules", "delete", "disconnect", "provider", "help", "version"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Errorf("commandNames lacks %q: %v", want, names)
		}
	}
	for _, n := range names {
		if n == "tui" {
			t.Errorf("the hidden default command %q must not be offered as a suggestion", n)
		}
	}
	if sug, ok := suggestCommand("tuii", names); ok {
		t.Errorf("a typo of the hidden command was suggested: %q", sug)
	}
}

// ── the corners the mutants found ───────────────────────────────────────────

// The words BEFORE a `--` are still checked; only what follows it is an explicit message.
func TestTheWordsBeforeTheDoubleDashAreStillChecked(t *testing.T) {
	wantUnknown(t, "sesions", "sessions", "sesions", "--", "x")
	wantUnknown(t, "sesions", "sessions", "--model", "a/b", "sesions", "--", "x")
	if got := parse("fix", "--", "sesions"); got.err != "" || got.prompt != "fix sesions" {
		t.Errorf("harness fix -- sesions: %+v, want the message \"fix sesions\"", got)
	}
}

// A real subcommand is the command; a typo check never runs on its arguments.
func TestACommandsArgumentsAreNeverCheckedAsATypo(t *testing.T) {
	for _, args := range [][]string{
		{"connect", "sesions"}, {"delete", "sesions"}, {"serve", "sesions"},
		{"memo", "sesions"}, {"memo", "mcpp"}, {"mcp", "add", "sesions", "--url", "http://x/mcp"},
	} {
		got := parse(args...)
		if strings.Contains(got.err, "unknown command") || got.prompt != "" {
			t.Errorf("harness %q: %+v — the typo check must not run on a real command's arguments", args, got)
		}
	}
}

// `help <word>` is a request for help only when <word> is a command a user can see; the hidden
// default command (the TUI) is not one.
func TestHelpIsOnlyForVisibleCommands(t *testing.T) {
	p, err := newParser()
	if err != nil {
		t.Fatal(err)
	}
	if isCommand(p, "tui") {
		t.Error("the hidden default command must not count as a command to ask help for")
	}
	for _, c := range []string{"serve", "sessions", "telegram", "acp"} {
		if !isCommand(p, c) {
			t.Errorf("%s is a visible command", c)
		}
	}
	for _, name := range []string{"help", "version", "tui", "nope", ""} {
		if isCommand(p, name) {
			t.Errorf("isCommand(%q) = true", name)
		}
	}
	// and so `help tui` is a message about "tui", like any other `help <not a command>`
	if got := parse("help", "tui"); got.err != "" || got.prompt != "help tui" || got.cmd != "tui <prompt>" {
		t.Errorf("harness help tui: %+v, want the message \"help tui\"", got)
	}
	if got := parse("help", "nope"); got.err != "" || got.prompt != "help nope" {
		t.Errorf("harness help nope: %+v", got)
	}
}

// suggestCommand against a list a user could end up with tomorrow: a name with capitals, and an
// alias. (Today's commands are all lowercase and have no top-level alias, which is why a mutant
// that broke either survived — the rule must not depend on that staying true.)
func TestSuggestCommandCopesWithCapitalsAndAliases(t *testing.T) {
	cmds := []string{"Deploy", "remove", "rm"}
	for _, c := range []struct {
		word, want string
		ok         bool
	}{
		{"deploy", "Deploy", true},  // the command has a capital, the typo does not
		{"deplooy", "Deploy", true}, // and with a typo
		{"DEPLOY", "Deploy", true},
		{"Deploy", "", false}, // spelled exactly: not a typo
		{"remov", "remove", true},
		{"rmm", "rm", true}, // an alias is a name too
		{"xyz", "", false},
	} {
		got, ok := suggestCommand(c.word, cmds)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("suggestCommand(%q) = %q,%v, want %q,%v", c.word, got, ok, c.want, c.ok)
		}
	}
}

// The swap rule needs BOTH letters to match crosswise: a lone coincidence is not a transposition.
func TestATranspositionNeedsBothLettersToCrossOver(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"ab", "ba", 1}, {"abcd", "acbd", 1}, {"abcd", "bacd", 1}, {"abcd", "abdc", 1}, // real swaps
		{"abx", "bay", 2}, {"abc", "bxa", 3}, {"ac", "ba", 2}, {"xa", "ab", 2}, // one crossing letter only
		{"aab", "aba", 1},
	} {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// Through Main — the real entry point — a mistyped command prints the message as written (no Kong
// indentation, no usage dump), on stderr, and exits 1; nothing reaches stdout.
func TestMainPrintsAMistypedCommandCleanly(t *testing.T) {
	stderr, stdout, code := runMain(t, "sesions")
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout is not empty: %q", stdout)
	}
	want := "harness: unknown command \"sesions\"\n  did you mean: harness sessions\n\nRun harness --help to list commands, or harness -- \"sesions\" to send it as a message (harness -p \"sesions\" for a one-shot answer).\n"
	if stderr != want {
		t.Errorf("stderr:\n got  %q\n want %q", stderr, want)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Errorf("a typo must not dump the usage: %q", stderr)
	}
	// the other errors still go through Kong's printer, with its usage
	if stderr, _, code := runMain(t, "--nope"); code == 0 || !strings.Contains(stderr, "unknown flag") {
		t.Errorf("--nope: code=%d stderr=%q", code, stderr)
	}
}

// exitCode is what the stand-in for os.Exit panics with, so runMain can recover the exit status.
type exitCode int

// runMain runs cli.Main with the process's stdout/stderr redirected, and returns what it wrote.
func runMain(t *testing.T, args ...string) (stderr, stdout string, code int) {
	t.Helper()
	return runMainIn(t, t.TempDir(), args...)
}

// runMainIn is runMain with HOME set to home, so a test can look at what the run left there (or
// check that its text does not mention it).
func runMainIn(t *testing.T, home string, args ...string) (stderr, stdout string, code int) {
	t.Helper()
	t.Setenv("HOME", home)
	oldExit := osExit
	osExit = func(c int) { panic(exitCode(c)) } // Kong ends the process after a usage error; do not die with it
	defer func() { osExit = oldExit }()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	os.Stdout, os.Stderr = wo, we
	done := make(chan [2]string)
	go func() {
		a, _ := io.ReadAll(ro)
		b, _ := io.ReadAll(re)
		done <- [2]string{string(a), string(b)}
	}()
	func() {
		defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
		defer func() {
			if r := recover(); r != nil {
				c, ok := r.(exitCode)
				if !ok {
					panic(r)
				}
				code = int(c)
			}
			wo.Close()
			we.Close()
		}()
		code = Main(args)
	}()
	got := <-done
	return got[1], got[0], code
}

// ── explicit flags are requirements (the CLI side) ──────────────────────────

// -p is a one-shot on an EPHEMERAL session: there is nothing to resume, so the flag is rejected —
// before any agent, session or model exists. It used to be dropped silently: the run answered on a
// brand new session and exited 0.
func TestOneShotRejectsResume(t *testing.T) {
	for name, args := range map[string][]string{
		"-p then --resume": {"-p", "x", "--resume", "abc"},
		"--resume then -p": {"--resume", "abc", "-p", "x"},
		"with --model too": {"--model", "a/b", "-p", "x", "--resume", "abc"},
		"long form":        {"--prompt", "x", "--resume=abc"},
	} {
		t.Run(name, func(t *testing.T) {
			got := parse(args...)
			if got.err != "--resume is not supported with -p" {
				t.Errorf("harness %q: err = %q, want %q", args, got.err, "--resume is not supported with -p")
			}
		})
	}
	home := t.TempDir()
	stderr, stdout, code := runMainIn(t, home, "-p", "x", "--resume", "abc")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "--resume is not supported with -p") {
		t.Errorf("through Main: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	// and nothing was created: Main returned before any agent existed, so nothing is on disk at all
	if entries, _ := os.ReadDir(filepath.Join(home, ".harness")); len(entries) != 0 {
		t.Errorf("the rejected run touched ~/.harness: %v", entries)
	}
}

// What is NOT rejected: --resume alone (the TUI), and -p with the flags that make sense.
func TestResumeAndOneShotStillWorkApart(t *testing.T) {
	if got := parse("--resume", "abc"); got.err != "" || got.resume != "abc" || got.cmd != "tui" {
		t.Errorf("--resume alone: %+v", got)
	}
	if got := parse("--resume", "abc", "task"); got.err != "" || got.resume != "abc" || got.prompt != "task" {
		t.Errorf("--resume with a prompt: %+v", got)
	}
	if got := parse("-p", "x", "--model", "a/b", "--thinking", "high"); got.err != "" || got.oneShot != "x" || got.model != "a/b" {
		t.Errorf("-p with --model and --thinking: %+v", got)
	}
	// an empty --resume is no --resume
	if got := parse("-p", "x", "--resume", ""); got.err != "" {
		t.Errorf("-p with an empty --resume: %+v", got)
	}
}

// A startup that cannot honor an explicit flag exits 1 BEFORE the TUI: the error goes to stderr as
// "error: …", stdout is empty (no banner, no escape sequence), and no model is ever called. This is
// the process-level contract; internal/tui proves the pieces.
func TestAnExplicitModelThatCannotBeUsedExitsOneWithNothingOnStdout(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown provider": {"--model", "zzz/no-existe", "the task"},
		"a bare word":      {"--model", "basura", "the task"},
		"no prompt":        {"--model", "zzz/no-existe"},
	} {
		t.Run(name, func(t *testing.T) {
			stderr, stdout, code := runMain(t, args...)
			if code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			if stdout != "" {
				t.Errorf("stdout is not empty: %q (a failed start must write nothing)", stdout)
			}
			if !strings.HasPrefix(stderr, "error: ") || !strings.Contains(stderr, "provider not found") {
				t.Errorf("stderr = %q, want `error: …provider not found…` (the text -p prints)", stderr)
			}
			if strings.Contains(stderr, "\x1b") {
				t.Errorf("an escape sequence on stderr: %q", stderr)
			}
		})
	}
}

func TestAMissingResumeIdExitsOneWithTheServersMessage(t *testing.T) {
	home := t.TempDir()
	for name, args := range map[string][]string{
		"with a prompt": {"--resume", "no-such-id", "the task"},
		"alone":         {"--resume", "no-such-id"},
	} {
		t.Run(name, func(t *testing.T) {
			stderr, stdout, code := runMainIn(t, home, args...)
			if code != 1 || stdout != "" {
				t.Errorf("code=%d stdout=%q", code, stdout)
			}
			if !strings.HasPrefix(stderr, "error: resume session no-such-id: ") || !strings.Contains(stderr, "not found") {
				t.Errorf("stderr = %q", stderr)
			}
		})
	}
	// no session file was created, whichever the run (searched under the real HOME it used)
	var files []string
	filepath.Walk(filepath.Join(home, ".harness", "agent", "sessions"), func(p string, fi os.FileInfo, err error) error { //nolint:errcheck
		if err == nil && !fi.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Errorf("a failed --resume left session files behind: %v", files)
	}
}

// The error text must carry no credentials or settings paths: it is the server's own message and
// names only what the person typed.
func TestTheFailureTextCarriesNoCredentialsOrSettingsPaths(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"--model", "zzz/x", "t"}, {"--resume", "nope", "t"}} {
		stderr, _, _ := runMainIn(t, home, args...)
		for _, bad := range []string{home, "settings.json", "credentials", "api_key", "apikey", "Bearer", "sk-"} {
			if strings.Contains(strings.ToLower(stderr), strings.ToLower(bad)) {
				t.Errorf("harness %v: the error mentions %q: %q", args, bad, stderr)
			}
		}
	}
}
