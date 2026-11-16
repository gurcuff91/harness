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
)

// Main is the application entry point. It parses os-style args (without the
// program name) and returns a process exit code. main() is expected to do no
// more than call this.
func Main(args []string) int {
	parser, err := newParser()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	ctx, err := parseArgs(parser, args)
	if err != nil {
		parser.FatalIfErrorf(err)
		return 1
	}

	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// newParser builds the Kong parser for the CLI grammar. It is the one place the parser is
// configured, so the tests parse exactly what Main parses.
func newParser(extra ...kong.Option) (*kong.Kong, error) {
	return kong.New(&CLI, append([]kong.Option{
		kong.Name("harness"),
		kong.Description("harness — fast terminal agent for coding & conversation"),
		kong.UsageOnError(),
		kong.Help(helpWithHiddenDefaultFlags),
		kong.ValueFormatter(enumValuesInHelp),
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
	ctx, err := parseSplit(parser, args)
	if err != nil {
		return nil, err
	}
	// -p is a one-shot run with no TUI; a positional prompt is the first message of the interactive
	// TUI. Together they ask for two different things, so it is an error rather than a guess.
	if selectsTUI(ctx) && CLI.TUI.Prompt != "" && CLI.TUI.initialPrompt() != "" {
		return nil, errors.New("use either -p (one-shot) or a positional prompt (interactive)")
	}
	return ctx, nil
}

func parseSplit(parser *kong.Kong, args []string) (*kong.Context, error) {
	for i, a := range args {
		if a != "--" {
			continue
		}
		head, tail := args[:i], args[i+1:]
		if ctx, err := parser.Parse(head); err == nil && selectsTUI(ctx) {
			CLI.TUI.Initial = append(CLI.TUI.Initial, tail...)
			return ctx, nil
		}
		break
	}
	return parser.Parse(args)
}

// selectsTUI reports whether the parsed command line runs the interactive TUI command (the root's
// default, with or without a positional prompt).
func selectsTUI(ctx *kong.Context) bool {
	c := ctx.Command()
	return c == "tui" || c == "tui <prompt>"
}

// signalContext returns a context cancelled on SIGINT/SIGTERM — the standard
// setup for commands that run until interrupted.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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
