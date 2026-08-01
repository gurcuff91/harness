// Run() for `harness serve <addr>` — starts the listener and hands it to
// the HTTP/SSE server, with careful Close() ordering against the instance
// registry (see the comment inline below).
package cli

import (
	"net"

	"github.com/gurcuff91/harness/internal/server"
)

func (c *serveCmd) Run() error {
	a := newInteractiveAgent(c.Scheduler)

	listener, err := net.Listen("tcp", c.Addr)
	if err != nil {
		a.Close()
		return err
	}
	srv := server.NewServer(a, server.ServerOptions{Verbose: true, Transport: "server"})

	// srv.Serve blocks until the listener closes, so Close() must be triggered
	// from elsewhere on Ctrl+C/SIGTERM. Close() itself calls httpSrv.Shutdown(),
	// which is what makes Serve() below return — so Serve() unblocking does NOT
	// mean Close() has finished (it unregisters the instance from
	// instances.json as its LAST step, after Shutdown). Run() must wait for
	// the Close() goroutine to actually complete before returning, otherwise
	// main()'s os.Exit races it and the instance registry entry is left behind.
	ctx, cancel := signalContext()
	defer cancel()
	closeDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		closeDone <- srv.Close() // graceful: sessions → agent → HTTP shutdown → unregister instance
	}()

	err = srv.Serve(listener)
	if ctx.Err() != nil {
		// Expected shutdown path (Ctrl+C/SIGTERM) — http.Serve returns
		// ErrServerClosed once Shutdown() completes. Block here until Close()
		// (started above) has fully finished, including instance unregistration.
		return <-closeDone
	}
	return err
}
