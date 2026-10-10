// Run() for `harness acp` — bridges the ACP transport onto stdin/stdout
// (acp.Run's defaults, so no options are needed here).
package cli

import (
	"github.com/gurcuff91/harness/transports/acp"
)

func (c *acpCmd) Run() error {
	a := newInteractiveAgent(false)
	srv, err := startTransportServer(a, "acp")
	if err != nil {
		a.Close()
		return err
	}
	defer srv.Close() // also closes the agent
	ctx, cancel := signalContext()
	defer cancel()
	return acp.Run(ctx, srv)
}
