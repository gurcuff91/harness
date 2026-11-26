package cli

import (
	"fmt"
	"net"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

// startInternalServer starts the HTTP server on a random port. Because we
// open the listener ourselves and hand it straight to Serve, the port is already
// accepting connections the instant net.Listen returns — no close-then-reopen
// race, so no readiness polling is needed. Always logx.NewNilLogger() — this
// server exists purely as the CLI's own private plumbing to talk to itself
// over HTTP/SSE for a single one-shot prompt; nobody else ever queries it,
// so its request log would be pure noise (matching the previous
// Verbose: false default exactly).
func startInternalServer(a *agent.Agent) (*internalServer, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("find port: %w", err)
	}
	addr := listener.Addr().String()

	srv := server.NewServer(a, server.ServerOptions{Logger: logx.NewNilLogger(), Frontend: "cli"})
	go srv.Serve(listener) //nolint:errcheck

	return &internalServer{srv: srv}, addr, nil
}

type internalServer struct {
	srv *server.Server
}

func (s *internalServer) Close() error {
	return s.srv.Close()
}

// startFrontendServer builds the server layer for a frontend command —
// agent → server → frontend: a loopback server over a, named after the frontend
// (e.g. "acp", reported by GET /api/server), that the frontend then runs on
// (acp.Run(ctx, srv)). The CLI owns both: closing the returned server also
// closes a (the frontend itself never closes either).
func startFrontendServer(a *agent.Agent, frontend string) (*server.Server, error) {
	return server.Start(a, "127.0.0.1:0", server.ServerOptions{Logger: logx.NewNilLogger(), Frontend: frontend})
}
