// Package cli is the harness command-line application: it parses arguments
// via the Kong grammar (kong.go + kong_run*.go) and dispatches to the
// matched command's Run() method. cmd/harness/main.go stays a thin entry
// point that just calls Main.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/gurcuff91/harness/internal/version"
)

// Main is the application entry point. It parses os-style args (without the
// program name) and returns a process exit code. main() is expected to do no
// more than call this.
func Main(args []string) int {
	parser, err := newParser(kong.Exit(osExit))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	ctx, err := parseArgs(parser, args)
	if err != nil {
		// A mistyped command is not a usage error: no usage dump, and a message laid out for a
		// person (Kong's own error printer would indent every line of it).
		var typo *unknownCommand
		if errors.As(err, &typo) {
			fmt.Fprintln(os.Stderr, typo.Error())
			return 1
		}
		parser.FatalIfErrorf(err)
		return 1
	}

	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// osExit is what Kong calls to end the process after it has printed a usage error or a help page.
// A variable so a test can run Main without dying with it.
var osExit = os.Exit

// newParser builds the Kong parser for the CLI grammar. It is the one place the parser is
// configured, so the tests parse exactly what Main parses.
func newParser(extra ...kong.Option) (*kong.Kong, error) {
	return kong.New(&CLI, append([]kong.Option{
		kong.Name("harness"),
		kong.Description("harness — fast terminal agent for coding & conversation"),
		kong.UsageOnError(),
		kong.Help(helpWithHiddenDefaultFlags),
		kong.ValueFormatter(enumValuesInHelp),
		kong.Vars{"version": version.Version},
	}, extra...)...)
}

// parseArgs parses the command line, with one rule Kong cannot express on its own: after `--`,
// everything is the initial prompt of the interactive TUI — even a word that is the name of a
// command.
//
// The grammar gives the root's default command (the TUI) an optional trailing positional prompt
// ("harness fix the login bug"), and a subcommand name WINS over a prompt: `harness sessions` lists
// sessions. That leaves no way to say "the prompt is the word serve". Kong's own `--` does not do
// it: at the root, Kong treats what follows `--` as positional only for a positional OF THE ROOT
// NODE, and the prompt belongs to the TUI command one level down, so `harness -- serve` would
// still run the serve command (verified against Kong, not assumed).
//
// So the arguments are split at the first `--`. If what comes BEFORE it selects the TUI (no
// subcommand named: bare, flags only, or leading prompt words), what comes after is appended to
// the prompt as it is. If it selects another command (`harness sessions -- x`), or does not parse
// at all, nothing changes and the whole line goes to Kong as before, so every error and every
// command behaves exactly as it did.
func parseArgs(parser *kong.Kong, args []string) (*kong.Context, error) {
	ctx, fromHead, err := parseSplit(parser, rewriteHelpVersion(parser, args))
	if err != nil {
		return nil, err
	}
	if !selectsTUI(ctx) {
		// A real command: its arguments are its own. (CLI.TUI.Initial is empty here anyway — the
		// positional belongs to the TUI node — so this guard is defensive, not load-bearing.)
		return ctx, nil
	}
	// -p is a one-shot run with no TUI; a positional prompt is the first message of the interactive
	// TUI. Together they ask for two different things, so it is an error rather than a guess.
	if CLI.TUI.Prompt != "" && CLI.TUI.initialPrompt() != "" {
		return nil, errors.New(`use either -p (one-shot) or a positional prompt (interactive)`)
	}
	// -p is a quick one-shot on an EPHEMERAL session (an in-memory store: it leaves nothing on disk),
	// so there is nothing to resume. Silently dropping the flag was the bug — it answered on a brand
	// new session and exited 0. This is a contradiction to reject, not a feature to support, and it
	// is rejected here, before any agent, session or model exists.
	if CLI.TUI.Prompt != "" && CLI.TUI.Resume != "" {
		return nil, errors.New(`--resume is not supported with -p`)
	}
	// A message whose FIRST word is one edit away from a command name is far more likely a mistyped
	// command than a message — and a message costs tokens. Same rule Claude Code applies; see
	// suggestCommand. Words after `--` are an explicit message and are never checked.
	if fromHead > 0 {
		if word := CLI.TUI.Initial[0]; word != "" {
			if sug, ok := suggestCommand(word, commandNames(parser)); ok {
				return nil, unknownCommandError(word, sug)
			}
		}
	}
	return ctx, nil
}

// parseSplit parses the line, splitting at the first `--` (see parseArgs). It also returns how many
// prompt words came from BEFORE the `--` (or from the whole line when there is none): only those
// are candidates for the mistyped-command check.
func parseSplit(parser *kong.Kong, args []string) (*kong.Context, int, error) {
	for i, a := range args {
		if a != "--" {
			continue
		}
		head, tail := args[:i], args[i+1:]
		if ctx, err := parser.Parse(head); err == nil && selectsTUI(ctx) {
			fromHead := len(CLI.TUI.Initial)
			CLI.TUI.Initial = append(CLI.TUI.Initial, tail...)
			return ctx, fromHead, nil
		}
		break
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		return nil, 0, err
	}
	return ctx, len(CLI.TUI.Initial), nil
}

// rewriteHelpVersion turns the two words people type to ask for help into the flags Kong already
// has: `harness help` is `--help`, `harness help serve` is `serve --help` (only for a real command:
// `harness help me fix this` stays a message), and `harness version` is `--version`.
func rewriteHelpVersion(parser *kong.Kong, args []string) []string {
	switch {
	case len(args) == 1 && args[0] == "help":
		return []string{"--help"}
	case len(args) == 2 && args[0] == "help" && isCommand(parser, args[1]):
		return []string{args[1], "--help"}
	case len(args) == 1 && args[0] == "version":
		return []string{"--version"}
	}
	return args
}

// commandNames are the names (and aliases) of the visible top-level commands, plus the two words
// rewriteHelpVersion understands — everything a mistyped first word could have been meant as.
func commandNames(parser *kong.Kong) []string {
	names := []string{"help", "version"}
	for _, c := range parser.Model.Children {
		if c.Type != kong.CommandNode || c.Hidden {
			continue
		}
		names = append(names, c.Name)
		names = append(names, c.Aliases...) // none at the top level today; a future one is a name too
	}
	return names
}

func isCommand(parser *kong.Kong, word string) bool {
	for _, c := range parser.Model.Children {
		if c.Type == kong.CommandNode && !c.Hidden && c.Name == word {
			return true
		}
	}
	return false
}

// suggestCommand reports the command a mistyped word was most likely meant as — and only if it
// really looks like a typo: within ONE edit, an insertion, a deletion, a substitution or the swap
// of two neighbouring letters (Damerau–Levenshtein, restricted), ignoring case.
//
// This is Claude Code's rule, measured against its real binary rather than guessed: `doctr`,
// `dctor`, `docotr`, `udpate`, `mcpp`, `mc`, `Mcpp` are caught for its commands; `doct` and `updt`
// (two edits away), `docters`, `xxdoctor`, and anything unrelated (`fix`, `OK`, `sesions` against
// its list) go through as the message they probably are. A threshold of two would swallow far too
// many real one-word messages; of zero, no typo at all.
func suggestCommand(word string, commands []string) (string, bool) {
	w := strings.ToLower(word)
	best, bestD := "", 2
	for _, c := range commands {
		lc := strings.ToLower(c)
		if w == lc && word == c {
			// Spelled exactly right: not a typo of anything. (It reaches here only when it was not
			// run as the command — `help me fix this` — and then it is the message it looks like.)
			return "", false
		}
		d := editDistance(w, lc)
		if d < bestD || (d == bestD && best != "" && c < best) {
			best, bestD = c, d
		}
	}
	return best, bestD <= 1
}

// editDistance is the optimal-string-alignment distance between a and b: the fewest insertions,
// deletions, substitutions and swaps of two ADJACENT letters (each counted once) that turn one into
// the other. It works on runes, so an accented letter is one edit, not two.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

// unknownCommand is the error for a first word that looks like a mistyped command (see
// suggestCommand). It is its own type so Main can print it as written.
type unknownCommand struct{ word, suggestion string }

// Error lays the message out like Claude Code's: what was wrong, the command it looks like, and the
// two ways out. A mistyped command costs no turn and no tokens, and exits 1.
func (e *unknownCommand) Error() string {
	return fmt.Sprintf("harness: unknown command %q\n  did you mean: harness %s\n\nRun harness --help to list commands, or harness -- %q to send it as a message (harness -p %q for a one-shot answer).",
		e.word, e.suggestion, e.word, e.word)
}

func unknownCommandError(word, suggestion string) error { return &unknownCommand{word, suggestion} }

// selectsTUI reports whether the parsed command line runs the interactive TUI command (the root's
// default, with or without a positional prompt).
func selectsTUI(ctx *kong.Context) bool {
	c := ctx.Command()
	return c == "tui" || c == "tui <prompt>"
}

// signalContext returns a context cancelled on SIGINT/SIGTERM/SIGHUP (a closed
// terminal hangs up: without it the process dies without closing its sessions,
// leaving its channel sockets and claims behind) — the standard
// setup for commands that run until interrupted.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
}

// helpWithHiddenDefaultFlags wraps kong.DefaultHelpPrinter so a command's own
// flags still show in its `--help`, even for the hidden-default-child
// pattern (TUI at the root; telegramRunCmd/slackRunCmd/settingsShowCmd one
// level down — see the parent-Run() note in kong.go). Kong never lets a
// struct declare both flags and cmd:"" children with its own Run() (that's
// exactly the double-Run()/leaked-flags bug the hidden-child pattern fixes),
// so those flags live one node down, on the hidden child, and would
// otherwise never appear in the parent's own help output.
//
// Only the exact node being displayed is patched — never its ancestors or
// its OTHER (non-default) children — so a subcommand like `slack login`
// never inherits `slack`'s flags; it only ever sees its own.
func helpWithHiddenDefaultFlags(options kong.HelpOptions, ctx *kong.Context) error {
	node := ctx.Selected()
	if node == nil {
		node = ctx.Model.Node
	}
	if node != nil && node.DefaultCmd != nil && node.DefaultCmd.Hidden {
		orig := node.Flags
		node.Flags = append(append([]*kong.Flag{}, node.Flags...), node.DefaultCmd.Flags...)
		defer func() { node.Flags = orig }()
		// The default command's positional (the TUI's first message) is part of how the root is
		// used — `harness "task"` — so it is shown with the root's own arguments as well.
		origPos := node.Positional
		node.Positional = append(append([]*kong.Value{}, node.Positional...), node.DefaultCmd.Positional...)
		defer func() { node.Positional = origPos }()
	}
	return kong.DefaultHelpPrinter(options, ctx)
}

// enumValuesInHelp wraps kong.DefaultHelpValueFormatter to append an enum
// flag's actual accepted values to its help text — e.g. "Thinking level
// (off|low|medium|high|xhigh|max)" — instead of making the user discover them
// only after a validation error. The empty string some of our enums accept
// as a sentinel for "use the settings default" (see the enum gotcha in
// kong.go) is never listed: it isn't a value the user would type, and
// showing it as a bare "|" would only confuse the reader.
func enumValuesInHelp(value *kong.Value) string {
	help := kong.DefaultHelpValueFormatter(value)
	if value.Enum == "" {
		return help
	}
	var vals []string
	for _, e := range value.EnumSlice() {
		if e != "" {
			vals = append(vals, e)
		}
	}
	if len(vals) == 0 {
		return help
	}
	suffix := "(" + strings.Join(vals, "|") + ")"
	if help == "" {
		return suffix
	}
	return help + " " + suffix
}
