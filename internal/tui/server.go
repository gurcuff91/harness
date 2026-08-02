package tui

import (
	"fmt"
	"net"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

// startInternalServer starts the HTTP transport on a random loopback port.
// tui talks to this in-process server exactly like an external client —
// keeping the frontend/backend separation clean. Always logx.NewNilLogger():
// this server shares stdout/stderr with the raw-mode terminal renderer, so
// ANY unconditional log line would corrupt the display — see
// server.SessionProxy's broadcast doc comment for the specific hazard this
// avoids.
func startInternalServer(a *agent.Agent) (*internalServer, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("find port: %w", err)
	}
	addr := listener.Addr().String()

	srv := server.NewServer(a, server.ServerOptions{Logger: logx.NewNilLogger(), Transport: "tui"})
	go srv.Serve(listener) //nolint:errcheck

	return &internalServer{srv: srv}, addr, nil
}

type internalServer struct {
	srv *server.Server
}

func (s *internalServer) Close() error {
	return s.srv.Close()
}
