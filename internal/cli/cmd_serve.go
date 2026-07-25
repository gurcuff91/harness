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
	defer a.Close()

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := server.NewServer(a, server.ServerOptions{Verbose: true})
	return srv.Serve(listener)
}
