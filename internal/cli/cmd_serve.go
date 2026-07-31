package cli

import (
	"flag"
	"net"

	"github.com/gurcuff91/harness/internal/server"
)

// cmdServe runs the HTTP/SSE server on the given address — a headless transport:
// an agent behind an API, with no UI of its own. Clients connect over HTTP/SSE
// and bring their own sessions. With --scheduler the process also runs the cron
// engine; a due schedule fires into its owner session if that session is
// currently active (via owner routing), otherwise it's skipped. The command
// builds the agent and hands it to the server.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	scheduler := fs.Bool("scheduler", false, "run the cron scheduler engine")
	// Reorder so flags parse regardless of position relative to the addr
	// (Go's flag package stops at the first non-flag argument).
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	addr := fs.Arg(0)
	if addr == "" {
		return errUsage("serve <addr> [--scheduler]")
	}

	a := newInteractiveAgent(*scheduler)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		a.Close()
		return err
	}
	srv := server.NewServer(a, server.ServerOptions{Verbose: true, Transport: "server"})

	// srv.Serve blocks until the listener closes, so Close() must be triggered
	// from elsewhere on Ctrl+C/SIGTERM. Close() itself calls httpSrv.Shutdown(),
	// which is what makes Serve() below return — so Serve() unblocking does NOT
	// mean Close() has finished (it unregisters the instance from
	// instances.json as its LAST step, after Shutdown). cmdServe must wait for
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
