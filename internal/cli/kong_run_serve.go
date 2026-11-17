// Run() for `harness serve <addr>` — delegates the listener/Serve/Close
// dance entirely to server.Run (see server/run.go for the full sequence).
package cli

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/server"
)

func (c *serveCmd) Run() error {
	a, err := c.newAgent()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	return server.Run(ctx, a, server.WithAddr(c.Addr), server.WithLogger(logx.NewHarnessLogger()))
}

// newAgent builds the agent `harness serve` runs: the shared interactive agent with the extras the command
// line and the environment asked for. It is Run minus the listener, so a test can build exactly what Run
// builds without opening a port.
func (c *serveCmd) newAgent() (*agent.Agent, error) {
	x, err := extrasOf(c.withFlags)
	if err != nil {
		return nil, err
	}
	return newInteractiveAgent(x), nil
}
