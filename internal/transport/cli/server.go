package cli

import (
	"fmt"
	"net"

	"github.com/gurcuff91/harness/agent"
	transporthttp "github.com/gurcuff91/harness/internal/transport/http"
)

// startInternalServer starts the HTTP transport on a random port. Because we
// open the listener ourselves and hand it straight to Serve, the port is already
// accepting connections the instant net.Listen returns — no close-then-reopen
// race, so no readiness polling is needed.
func startInternalServer(a *agent.Agent) (*internalServer, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("find port: %w", err)
	}
	addr := listener.Addr().String()

	srv := transporthttp.NewServer(a, transporthttp.ServerOptions{Verbose: false})
	go srv.Serve(listener) //nolint:errcheck

	return &internalServer{srv: srv}, addr, nil
}

type internalServer struct {
	srv *transporthttp.Server
}

func (s *internalServer) Close() error { return nil }
