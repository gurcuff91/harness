// Run() for `harness acp` — bridges the ACP frontend onto stdin/stdout
// (acp.Run's defaults, so no options are needed here).
package cli

import (
	"github.com/gurcuff91/harness/acp"
	"github.com/gurcuff91/harness/agent"
)

func (c *acpCmd) Run() error {
	a, err := c.newAgent()
	if err != nil {
		return err
	}
	srv, err := startFrontendServer(a, "acp")
	if err != nil {
		a.Close()
		return err
	}
	defer srv.Close() // also closes the agent
	ctx, cancel := signalContext()
	defer cancel()
	return acp.Run(ctx, srv)
}

// newAgent builds the agent `harness acp` runs. ACP has no flags to give: an editor starts this exact
// command as a sub-process and negotiates everything else over the protocol, so the environment is the only
// way to switch the extras on. It is Run minus the stdio bridge, so a test can build exactly what Run builds.
func (c *acpCmd) newAgent() (*agent.Agent, error) {
	x, err := extrasOf(withFlags{})
	if err != nil {
		return nil, err
	}
	return newInteractiveAgent(x), nil
}
