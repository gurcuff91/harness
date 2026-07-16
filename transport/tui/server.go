package tui

import (
	"fmt"
	"net"

	"github.com/gurcuff91/harness/agent"
	transporthttp "github.com/gurcuff91/harness/transport/http"
)

// startInternalServer starts the HTTP transport on a random loopback port.
// tui talks to this in-process server exactly like an external client —
// keeping the frontend/backend separation clean.
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

func (s *internalServer) Close() error {
	return nil
}
