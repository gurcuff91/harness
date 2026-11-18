// Run() for the root tuiCmd — the implicit default command (bare `harness`,
// or `harness --model x`) that also doubles as the one-shot prompt runner
// (`harness -p "..."`, via the Prompt/-p flag): Prompt set → one-shot Run()
// (cli.go); otherwise → interactive tui.Run, with the positional prompt (if any) as its first message.
package cli

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/tui"
	"github.com/gurcuff91/harness/transports/slack"
)

func (c *tuiCmd) Run() error {
	ctx, cancel := signalContext()
	defer cancel()

	if c.Prompt != "" {
		a := newOneShotAgent()
		defer a.Close()
		return Run(ctx, a, c.Prompt, Opts{Model: c.Model, Thinking: c.Thinking, Output: c.Output})
	}

	// The extras are decided ONCE, here, and the same answer builds the agent and tells the TUI whether the
	// scheduler is on (its footer badge): reading the flags and the environment twice could disagree.
	x, err := extrasOf(c.withFlags)
	if err != nil {
		return err
	}
	chans := storedChannels(slack.LoadCredentials, false) // never log to the terminal the TUI draws on
	inputs, err := slackInputChannels(c.SlackChannels, chans)
	if err != nil {
		return err
	}
	a := c.newTUIAgent(x, chans...)
	defer a.Close()

	t := c.newTUI(a, x)
	t.SetChannels(inputs)
	return t.Run(ctx)
}

// newTUIAgent builds the agent the interactive TUI drives: the shared interactive agent plus the
// instruction blocks only the TUI adds (see tuiDirectives). It is the one place Run gets its agent
// from, so a test can build exactly what Run builds, without a terminal.
func (c *tuiCmd) newTUIAgent(x extras, chans ...agent.Channel) *agent.Agent {
	opts := interactiveOptions(x, tuiDirectives())
	opts.Channels = chans
	return agent.New(opts)
}

// newTUI builds the interactive TUI for the parsed command line: the flags, the scheduler switch and
// the positional prompt (the first message, submitted once the session is ready). It is Run minus
// the part that needs a terminal, so the wiring from the command line to the TUI can be tested.
func (c *tuiCmd) newTUI(a *agent.Agent, x extras) *tui.TUI {
	t := tui.New(a)
	t.SetFlags(c.Model, c.Thinking, c.Resume)
	t.SetInitialPrompt(c.initialPrompt())
	t.SetScheduler(x.scheduler)
	return t
}

// tuiDirectives are the instruction blocks only the interactive TUI adds to its sessions' system
// prompt: today, the orientation for running inside Herdr (see tui.HerdrDirective), which is empty
// anywhere else. They belong here and not in newInteractiveAgent because that constructor is shared
// by serve, telegram, slack and acp, and none of those receives Herdr's <hc> envelope.
func tuiDirectives() []string {
	if d := tui.HerdrDirective(); d != "" {
		return []string{d}
	}
	return nil
}
