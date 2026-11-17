package cli

import (
	"fmt"
	"os"
	"strings"
)

// The optional capabilities the CLI can give an agent, and the one place that decides whether each is on.
//
// The agent the CLI builds is deliberately light: the base tools and nothing else. Two extras are opt-in,
// because each costs every session something whether it is used or not (measured: colleagues add 2 509
// characters to the system prompt and 3 031 bytes of tool definitions to EVERY request, the scheduler 343
// characters and 1 585 bytes), and most runs use neither:
//
//   - colleagues: ColleagueList / ColleagueAsk, to find and ask OTHER running harness instances;
//   - scheduler: the cron engine that fires scheduled prompts, and the tools that create them.
//
// Each is switched on by a flag (--with-colleagues, --with-scheduler) OR by an environment variable
// (HARNESS_WITH_COLLEAGUES, HARNESS_WITH_SCHEDULER), either one being enough. The variable exists for the
// commands that cannot be given flags — `harness acp` is started by another program (an editor) — and for
// services and containers where setting the environment is easier than editing a command line.
//
// WebSearch is NOT an extra: every agent the CLI builds for real work has it, with no switch.
//
// Turning colleagues off does not make an instance invisible: the instance REGISTRY is written by the
// server (server.RegisterInstance), not by the tools, so a harness started without --with-colleagues is still
// listed and still answers what other agents ask it. It only lacks the tools to ask THEM.

const (
	envWithColleagues = "HARNESS_WITH_COLLEAGUES"
	envWithScheduler  = "HARNESS_WITH_SCHEDULER"
)

// withFlags is the pair of switches, declared once and embedded by every command that builds an agent
// (the TUI, serve, slack, telegram), so the names and the help text cannot drift apart between them. They
// are plain booleans on the command line: absent means "not given", and the environment is consulted only
// then (see extrasFrom).
type withFlags struct {
	WithColleagues bool `name:"with-colleagues" help:"Give the agent ColleagueList/ColleagueAsk, to find and ask other running harness instances (also: HARNESS_WITH_COLLEAGUES=1)"`
	WithScheduler  bool `name:"with-scheduler" help:"Run the cron scheduler engine, and give the agent the tools that create schedules (also: HARNESS_WITH_SCHEDULER=1)"`
}

// extras is what a command asked for, after the flags and the environment have both been read.
type extras struct {
	colleagues bool
	scheduler  bool
}

// extrasFrom combines the flags with the environment. A flag that was given wins and switches the extra on:
// there is no "--without", so a flag can only turn something on, and the environment can only add to it.
// getenv is a parameter so a test never depends on the machine's environment.
func extrasFrom(f withFlags, getenv func(string) string) (extras, error) {
	col, err := envSwitch(envWithColleagues, getenv)
	if err != nil {
		return extras{}, err
	}
	sch, err := envSwitch(envWithScheduler, getenv)
	if err != nil {
		return extras{}, err
	}
	return extras{
		colleagues: f.WithColleagues || col,
		scheduler:  f.WithScheduler || sch,
	}, nil
}

// envSwitch reads a boolean environment variable. Unset, empty and blank all mean "off" — an empty value
// is very common (`export X=`, a .env line with nothing after the `=`, an unset `${X}` in a unit file) and
// must never stop a command from starting. 1, true and yes (any case) mean "on"; 0, false and no mean
// "off". Anything else is an error that names the variable and what it may be, instead of being read as
// either answer: a typo in a deployment should not silently leave a feature on or off.
func envSwitch(name string, getenv func(string) string) (bool, error) {
	v := strings.TrimSpace(getenv(name))
	switch strings.ToLower(v) {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	}
	return false, fmt.Errorf("%s=%q: use 1, true, yes, 0, false or no (or leave it empty)", name, v)
}

// extrasOf is extrasFrom over the real environment.
func extrasOf(f withFlags) (extras, error) { return extrasFrom(f, os.Getenv) }
