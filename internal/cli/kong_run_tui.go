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

	a := newInteractiveAgent(c.Scheduler)
	defer a.Close()

	return c.newTUI(a).Run(ctx)
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
