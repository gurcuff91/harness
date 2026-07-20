package cli

import (
	"flag"
	"fmt"
)

// errUsage builds a standard "usage: harness <spec>" error.
func errUsage(spec string) error { return fmt.Errorf("usage: harness %s", spec) }

// errf is a small fmt.Errorf alias for command handlers.
func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }

// reorderFlags moves flag arguments (leading '-') ahead of positional ones so a
// flag set parses them regardless of where the user put them relative to a
// positional arg — Go's flag package otherwise stops at the first non-flag.
// Only handles boolean-style flags safely; sufficient for commands whose flags
// are all bools (e.g. serve --scheduler). Order within each group is preserved.
func reorderFlags(args []string) []string {
	var flags, positional []string
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return append(flags, positional...)
}

// ── one-shot prompt ────────────────────────────────────────────────────────

// cmdPrompt is `harness -p <prompt> [--model ...] [--thinking ...] [--output ...]`.
func cmdPrompt(args []string) error {
	if len(args) == 0 {
		return errUsage("-p <prompt> [--model ...] [--thinking ...] [--output ...]")
	}
	prompt := args[0]
	fs := flag.NewFlagSet("prompt", flag.ContinueOnError)
	model := fs.String("model", "", "model to use")
	thinking := fs.String("thinking", "", "thinking level")
	output := fs.String("output", "text", "output mode: text|json|json-stream")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return Run(ctx, a, prompt, Opts{Model: *model, Thinking: *thinking, Output: *output})
}

// ── providers ──────────────────────────────────────────────────────────────

func cmdProviders(args []string) error {
	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunProviders(ctx, a, "text")
}

func cmdConnect(args []string) error {
	if len(args) == 0 {
		return errUsage("connect <provider> [api_key]")
	}
	apiKey := ""
	if len(args) > 1 {
		apiKey = args[1]
	}
	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunConnect(ctx, a, args[0], apiKey, "text")
}

func cmdDisconnect(args []string) error {
	if len(args) == 0 {
		return errUsage("disconnect <provider>")
	}
	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunDisconnect(ctx, a, args[0], "text")
}

// ── sessions ───────────────────────────────────────────────────────────────

func cmdSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	all := fs.Bool("all", false, "list sessions across all directories")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunSessions(ctx, a, *all, "text")
}

func cmdDelete(args []string) error {
	if len(args) == 0 {
		return errUsage("delete <session_id>")
	}
	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunDelete(ctx, a, args[0], "text")
}

// ── settings ───────────────────────────────────────────────────────────────

func cmdSettings(args []string) error {
	a := newConfigAgent()
	ctx, cancel := signalContext()
	defer cancel()

	if len(args) == 0 {
		return RunSettings(ctx, a, "text")
	}
	if args[0] == "set" {
		if len(args) < 3 {
			return errUsage("settings set <model|thinking> <value>")
		}
		return RunSettingsSet(ctx, a, args[1], args[2], "text")
	}
	return errf("unknown settings subcommand: %s\nusage: harness settings [set <key> <value>]", args[0])
}

// ── schedules ──────────────────────────────────────────────────────────────

func cmdSchedules(args []string) error {
	fs := flag.NewFlagSet("schedules", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	output := "text"
	if *asJSON {
		output = "json"
	}
	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunSchedules(ctx, a, output)
}
