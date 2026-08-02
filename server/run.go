package server

import (
	"context"
	"net"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/logx"
)

// Option configures a Run call.
type Option func(*runConfig)

type runConfig struct {
	addr   string
	logger logx.Logger
}

// WithAddr sets the listen address (e.g. "127.0.0.1:8080" or ":8080" for all
// interfaces). Default: "127.0.0.1:0" — loopback only, OS-assigned ephemeral
// port, exactly like every in-process server a transport starts for itself
// (see transports/telegram, transports/slack, transports/acp).
func WithAddr(addr string) Option {
	return func(c *runConfig) { c.addr = addr }
}

// WithLogger sets the Logger that receives request/lifecycle log lines.
// Default: logx.NewNilLogger() (silent) — an SDK consumer that never configures
// one gets no output at all; harness's own CLI always passes
// internal/logx.NewHarnessLogger() explicitly (see internal/cli/kong_run_serve.go).
func WithLogger(l logx.Logger) Option {
	return func(c *runConfig) { c.logger = l }
}

// Run starts the HTTP/SSE server on top of an already-built agent and blocks
// until ctx is cancelled, then performs a graceful shutdown before
// returning — the exact steps `harness serve` used to perform by hand
// (net.Listen, Serve in a goroutine, wait for ctx, Close in the right order):
// close all active sessions, close the agent (MCP subprocesses, memory DB,
// scheduler engine, store), shut down the HTTP server, and unregister the
// instance from ~/.harness/instances.json. Run only returns once that full
// sequence has finished — a caller that exits right after Run returns (e.g.
// the CLI's os.Exit) is guaranteed not to race the instance-registry cleanup.
//
// Returns nil on an expected shutdown (ctx cancelled — Ctrl+C/SIGTERM in the
// CLI, or a caller-driven cancel() in an embedding SDK use), or the first
// error encountered otherwise (failure to bind the listener, or a non-clean
// HTTP server error).
func Run(ctx context.Context, a *agent.Agent, opts ...Option) error {
	cfg := runConfig{addr: "127.0.0.1:0", logger: logx.NewNilLogger()}
	for _, opt := range opts {
		opt(&cfg)
	}

	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		a.Close() //nolint:errcheck — best-effort: nothing was started yet, this just releases MCP/memory/store
		return err
	}

	srv := NewServer(a, ServerOptions{Logger: cfg.logger, Transport: "server"})

	// srv.Serve blocks until the listener closes, so Close() must be
	// triggered from elsewhere on ctx cancellation. Close() itself calls
	// httpSrv.Shutdown(), which is what makes Serve() below return — so
	// Serve() unblocking does NOT mean Close() has finished (it unregisters
	// the instance from instances.json as its LAST step, after Shutdown).
	// Run must wait for the Close() goroutine to actually complete before
	// returning — see the doc comment above for why.
	closeDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		closeDone <- srv.Close() // graceful: sessions → agent → HTTP shutdown → unregister instance
	}()

	err = srv.Serve(listener)
	if ctx.Err() != nil {
		// Expected shutdown path — http.Serve returns ErrServerClosed once
		// Shutdown() completes. Block here until Close() (started above) has
		// fully finished, including instance unregistration.
		return <-closeDone
	}
	return err
}
