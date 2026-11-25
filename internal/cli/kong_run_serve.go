// Run() for `harness serve <addr>` — delegates the listener/Serve/Close
// dance entirely to server.Run (see server/run.go for the full sequence).
package cli

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/gurcuff91/harness/agent"
	slackch "github.com/gurcuff91/harness/channels/slack"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

func (c *serveCmd) Run() error {
	addr, err := serveAddr(c.Addr)
	if err != nil {
		return err
	}
	secret := serveSecret(c.Secret, os.Getenv)
	if err := checkServeSecurity(addr, secret, c.Insecure); err != nil {
		return err
	}
	a, err := c.newAgent()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	logger := logx.NewHarnessLogger()
	if secret == "" {
		logger.Warn("server", "insecure", "addr", addr, "reason", "no secret: anyone who can reach this address can use the sessions, the connected providers and the tools",
			"fix", "harness serve --secret $(harness gen-secret) (or HARNESS_SECRET)")
	}
	return server.Run(ctx, a, server.WithAddr(addr), server.WithLogger(logger), server.WithSecret(secret))
}

// SecretEnv supplies the server's secret from the environment (a flag shows in
// the process list; a variable does not).
const SecretEnv = "HARNESS_SECRET"

// serveSecret is the secret `harness serve` runs with: the flag, else HARNESS_SECRET.
func serveSecret(flag string, getenv func(string) string) string {
	if s := strings.TrimSpace(flag); s != "" {
		return s
	}
	return strings.TrimSpace(getenv(SecretEnv))
}

// checkServeSecurity refuses to start a server that is reachable from the
// network with no secret — unless --insecure says it is meant: the server runs
// Bash and holds the connected providers, so exposing it by forgetting one
// flag must not work. Listening on this machine only needs nothing.
func checkServeSecurity(addr, secret string, insecure bool) error {
	if secret != "" || insecure || isLoopbackAddr(addr) {
		return nil
	}
	return fmt.Errorf("refusing to listen on %s without a secret: anyone who reaches it could use your sessions, providers and tools. Use --secret (make one with: harness gen-secret; or set %s), or --insecure to expose it anyway", addr, SecretEnv)
}

// isLoopbackAddr reports whether host:port listens on this machine only.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serveAddr is the address `harness serve` listens on. A missing host (":8080")
// means THIS MACHINE ONLY (127.0.0.1), not every interface as Go would read it:
// the server must be asked explicitly to be reachable from the network
// ("0.0.0.0:8080", "[::]:8080" or a real address), and then it wants a secret.
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

func (c *genSecretCmd) Run() error {
	fmt.Println(server.GenerateServerSec())
	return nil
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
