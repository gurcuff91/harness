// Run() for the root tuiCmd — the implicit default command (bare `harness`,
// or `harness --model x`) that also doubles as the one-shot prompt runner
// (`harness -p "..."`, via the Prompt/-p flag): Prompt set → one-shot Run()
// (cli.go); otherwise → interactive tui.Run, with the positional prompt (if any) as its first message.
package cli

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/tui"
)

func (c *tuiCmd) Run() error {
	ctx, cancel := signalContext()
	defer cancel()

	if c.Prompt != "" {
		a := newOneShotAgent()
		defer a.Close()
		return Run(ctx, a, c.Prompt, Opts{Model: c.Model, Thinking: c.Thinking, Output: c.Output})
	}

	a := c.newTUIAgent()
	defer a.Close()

	return c.newTUI(a).Run(ctx)
}

// newTUIAgent builds the agent the interactive TUI drives: the shared interactive agent plus the
// instruction blocks only the TUI adds (see tuiDirectives). It is the one place Run gets its agent
// from, so a test can build exactly what Run builds, without a terminal.
func (c *tuiCmd) newTUIAgent() *agent.Agent {
	return newInteractiveAgent(c.Scheduler, tuiDirectives()...)
}

// newTUI builds the interactive TUI for the parsed command line: the flags, the scheduler switch and
// the positional prompt (the first message, submitted once the session is ready). It is Run minus
// the part that needs a terminal, so the wiring from the command line to the TUI can be tested.
func (c *tuiCmd) newTUI(a *agent.Agent) *tui.TUI {
	t := tui.New(a)
	t.SetFlags(c.Model, c.Thinking, c.Resume)
	t.SetInitialPrompt(c.initialPrompt())
	t.SetScheduler(c.Scheduler)
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
