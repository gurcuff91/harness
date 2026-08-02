// Run() for `harness acp` — bridges the ACP transport onto stdin/stdout
// (acp.Run's defaults, so no options are needed here).
package cli

import (
	"github.com/gurcuff91/harness/transports/acp"
)

func (c *acpCmd) Run() error {
	a := newInteractiveAgent(false)
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return acp.Run(ctx, a)
}
