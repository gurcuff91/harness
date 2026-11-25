// Run() for `harness serve <addr>` — delegates the listener/Serve/Close
// dance entirely to server.Run (see server/run.go for the full sequence).
package cli

import (
	"fmt"
	"net"
	"strings"

	"github.com/gurcuff91/harness/agent"
	slackch "github.com/gurcuff91/harness/channels/slack"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

func (c *serveCmd) Run() error {
	a, err := c.newAgent()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	addr, err := serveAddr(c.Addr)
	if err != nil {
		return err
	}
	return server.Run(ctx, a, server.WithAddr(addr), server.WithLogger(logx.NewHarnessLogger()))
}

// serveAddr is the address `harness serve` listens on. A missing host (":8080")
// means THIS MACHINE ONLY (127.0.0.1), not every interface as Go would read it:
// the server has no authentication, so reaching it from the network must be
// asked for explicitly ("0.0.0.0:8080", "[::]:8080" or a real address).
func serveAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "", fmt.Errorf("invalid address %q: use :PORT (this machine only) or HOST:PORT (e.g. 0.0.0.0:8080 to expose it)", addr)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

// newAgent builds the agent `harness serve` runs: the shared interactive agent with the extras the command
// line and the environment asked for. It is Run minus the listener, so a test can build exactly what Run
// builds without opening a port.
func (c *serveCmd) newAgent() (*agent.Agent, error) {
	x, err := extrasOf(c.withFlags)
	if err != nil {
		return nil, err
	}
	opts := interactiveOptions(x, nil)
	// Clients bind sessions to conversations through the API (channels on
	// create/resume); serve itself takes no channel flag.
	opts.Channels = append(opts.Channels, storedChannels(slackch.LoadCredentials, true)...)
	opts.Channels = append(opts.Channels, telegramChannels(c.TelegramToken, true)...)
	return agent.New(opts), nil
}
