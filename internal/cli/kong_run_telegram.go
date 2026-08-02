// Run() methods for `harness telegram` and its nested subcommands
// (pair/unpair/list). Each Run() below is a thin adapter over the
// telegram.* package functions and telegram.Run.
package cli

import (
	"github.com/gurcuff91/harness/transports/telegram"
)

func (c *telegramRunCmd) Run() error {
	// c.Scheduler decides the AGENT's own scheduler engine here — it's an
	// agent.AgentOptions.EnableScheduler concern, not something
	// telegram.Options carries (see its doc comment for why).
	a := newInteractiveAgent(c.Scheduler, telegram.Directive)
	ctx, cancel := signalContext()
	defer cancel()

	opts := []telegram.Option{telegram.WithToken(c.Token)}
	if c.Model != "" {
		opts = append(opts, telegram.WithSessionModel(c.Model))
	}
	if c.Thinking != "" {
		opts = append(opts, telegram.WithSessionThinking(c.Thinking))
	}
	if c.AllowUnpair {
		opts = append(opts, telegram.WithAllowUnpair())
	}
	return telegram.Run(ctx, a, opts...)
}

func (c *telegramPairCmd) Run() error {
	return telegram.Pair(c.ChatID)
}

func (c *telegramUnpairCmd) Run() error {
	return telegram.Unpair(c.ChatID)
}

func (c *telegramListCmd) Run() error {
	return telegram.ListPaired()
}
