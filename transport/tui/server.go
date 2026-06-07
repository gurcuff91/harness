package tui

import (
	"fmt"
	"net"

	"github.com/gurcuff91/harness/agent"
	transporthttp "github.com/gurcuff91/harness/transport/http"
)

// startInternalServer starts the HTTP transport on a random port.
// Returns the server, address, and error.
func startInternalServer(a *agent.Agent) (*internalServer, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("find port: %w", err)
	}
	addr := listener.Addr().String()

	srv := transporthttp.NewServer(a, transporthttp.ServerOptions{Verbose: false})

	go func() {
		// chi doesn't support net.Listener directly, so we use the port
		listener.Close()
		srv.ListenAndServe(addr) //nolint:errcheck
	}()

	return &internalServer{srv: srv}, addr, nil
}

type internalServer struct {
	srv *transporthttp.Server
}

func (s *internalServer) Close() error {
	// TODO: graceful shutdown
	return nil
}
