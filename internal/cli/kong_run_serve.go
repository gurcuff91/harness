// Run() for `harness serve <addr>` — delegates the listener/Serve/Close
// dance entirely to server.Run (see server/run.go for the full sequence).
package cli

import (
	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/server"
)

func (c *serveCmd) Run() error {
	a := newInteractiveAgent(c.Scheduler)
	ctx, cancel := signalContext()
	defer cancel()
	return server.Run(ctx, a, server.WithAddr(c.Addr), server.WithLogger(logx.HarnessLogger{}))
}
