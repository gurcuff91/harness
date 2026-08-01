// Run() methods for `harness telegram` and its nested subcommands
// (pair/unpair/list). Each Run() below is a thin adapter over the
// telegram.* package functions and telegram.Run.
package cli

import (
	"github.com/gurcuff91/harness/internal/transport/telegram"
)

func (c *telegramRunCmd) Run() error {
	a := newInteractiveAgent(c.Scheduler, telegram.Directive)
	ctx, cancel := signalContext()
	defer cancel()

	return telegram.Run(ctx, a, telegram.Options{
		Token:       c.Token,
		Model:       c.Model,
		Thinking:    c.Thinking,
		Scheduler:   c.Scheduler,
		AllowUnpair: c.AllowUnpair,
	})
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
