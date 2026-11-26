// Run() for `harness rpc` — the JSON-RPC protocol over stdin/stdout.
package cli

import (
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/rpc"
)

// Run serves the protocol until the client closes stdin (or a signal). stdout
// carries ONLY protocol messages; the logger writes to stderr (log's default),
// so diagnostics can never corrupt the stream. The agent is built by the
// client's `initialize`, never here.
func (c *rpcCmd) Run() error {
	ctx, cancel := signalContext()
	defer cancel()
	return rpc.Run(ctx, rpc.WithLogger(logx.NewHarnessLogger()))
}
