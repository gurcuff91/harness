// Run() for `harness acp` — bridges the ACP transport onto stdin/stdout.
package cli

import (
	"os"

	"github.com/gurcuff91/harness/internal/transport/acp"
)

func (c *acpCmd) Run() error {
	a := newInteractiveAgent(false)
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return acp.Run(ctx, a, os.Stdin, os.Stdout)
}
