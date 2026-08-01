// Package cli is the harness command-line application: it parses arguments
// via the Kong grammar (kong.go + kong_run*.go) and dispatches to the
// matched command's Run() method. cmd/harness/main.go stays a thin entry
// point that just calls Main.
package cli

import (
	"context"
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
	parser, err := kong.New(&CLI,
		kong.Name("harness"),
		kong.Description("harness — fast terminal agent for coding & conversation"),
		kong.UsageOnError(),
		kong.Help(helpWithHiddenDefaultFlags),
		kong.ValueFormatter(enumValuesInHelp),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	ctx, err := parser.Parse(args)
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
	}
	return kong.DefaultHelpPrinter(options, ctx)
}

// enumValuesInHelp wraps kong.DefaultHelpValueFormatter to append an enum
// flag's actual accepted values to its help text — e.g. "Thinking level
// (off|low|medium|high|xhigh)" — instead of making the user discover them
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
