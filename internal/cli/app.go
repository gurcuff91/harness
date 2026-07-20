// Package cli is the harness command-line application: it parses arguments and
// dispatches to per-command handlers. Each command owns its own flag set and
// builds whatever agent/transport it needs, so cmd/harness/main.go stays a thin
// entry point.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// Main is the application entry point. It parses os-style args (without the
// program name) and returns a process exit code. main() is expected to do no
// more than call this.
func Main(args []string) int {
	// --help / -h anywhere → usage.
	for _, a := range args {
		if a == "--help" || a == "-h" {
			printHelp()
			return 0
		}
	}

	// No args → interactive TUI.
	if len(args) == 0 {
		return run(cmdTUI(nil))
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return run(cmdServe(rest))
	case "telegram":
		return run(cmdTelegram(rest))
	case "providers":
		return run(cmdProviders(rest))
	case "connect":
		return run(cmdConnect(rest))
	case "disconnect":
		return run(cmdDisconnect(rest))
	case "sessions":
		return run(cmdSessions(rest))
	case "delete":
		return run(cmdDelete(rest))
	case "settings":
		return run(cmdSettings(rest))
	case "mcp":
		return run(cmdMCP(rest))
	case "memo":
		return run(cmdMemo(rest))
	case "schedules":
		return run(cmdSchedules(rest))
	case "-p", "--prompt":
		return run(cmdPrompt(rest))
	default:
		// Leading-dash first arg → TUI flags (e.g. `harness --model x`,
		// `harness --resume <id>`). cmdTUI's flag set handles --resume itself.
		if len(cmd) > 0 && cmd[0] == '-' {
			return run(cmdTUI(args))
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nRun 'harness --help' for usage.\n", cmd)
		return 1
	}
}

// run turns a command's error into an exit code, printing it to stderr.
func run(err error) int {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// signalContext returns a context cancelled on SIGINT/SIGTERM — the standard
// setup for commands that run until interrupted.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}
