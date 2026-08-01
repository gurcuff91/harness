// Run() for the root tuiCmd — the implicit default command (bare `harness`,
// or `harness --model x`) that also doubles as the one-shot prompt runner
// (`harness -p "..."`, via the Prompt/-p flag): Prompt set → one-shot Run()
// (cli.go); otherwise → interactive tui.Run.
package cli

import (
	"github.com/gurcuff91/harness/internal/transport/tui"
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

	t := tui.New(a)
	t.SetFlags(c.Model, c.Thinking, c.Resume)
	t.SetScheduler(c.Scheduler)
	return t.Run(ctx)
}
