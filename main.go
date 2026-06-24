package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/transport/cli"
	httptransport "github.com/gurcuff91/harness/transport/http"
	// tui-v1 (tview-based, reference — do not delete):
	// "github.com/gurcuff91/harness/transport/tui"
	tui "github.com/gurcuff91/harness/transport/tui-v3"
)

func main() {
	args := os.Args[1:]

	// --help or -h anywhere
	for _, a := range args {
		if a == "--help" || a == "-h" {
			printHelp()
			return
		}
	}

	// No args → TUI
	if len(args) == 0 {
		runTUI("", "", "")
		return
	}

	// Dispatch by first argument
	switch args[0] {
	case "http":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness http <addr>")
			os.Exit(1)
		}
		runHTTP(args[1])

	case "providers":
		runProviders(args[1:])

	case "connect":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness connect <provider> [api_key]")
			os.Exit(1)
		}
		apiKey := ""
		if len(args) > 2 {
			apiKey = args[2]
		}
		runConnect(args[1], apiKey)

	case "disconnect":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness disconnect <provider>")
			os.Exit(1)
		}
		runDisconnect(args[1])

	case "sessions":
		all := false
		for _, a := range args[1:] {
			if a == "--all" {
				all = true
			}
		}
		runSessions(all)

	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness delete <session_id>")
			os.Exit(1)
		}
		runDelete(args[1])

	case "-p", "--prompt":
		// CLI prompt mode (explicit)
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness -p <prompt> [--model ...] [--thinking ...] [--output ...]")
			os.Exit(1)
		}
		model, thinking, output := extractPromptFlags(args[2:])
		runCLI(args[1], model, thinking, output)

	case "--resume":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness --resume <id> [--model ...] [--thinking ...]")
			os.Exit(1)
		}
		model, thinking := extractFlags(args[2:])
		runTUI(model, thinking, args[1])

	default:
		// Flags for TUI
		if len(args[0]) > 0 && args[0][0] == '-' {
			model, thinking, resumeID := extractAllFlags(args)
			if resumeID != "" {
				runTUI(model, thinking, resumeID)
				return
			}
			runTUI(model, thinking, "")
			return
		}
		// Unknown command
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nRun 'harness --help' for usage.\n", args[0])
		os.Exit(1)
	}
}

// ── Dispatchers ──────────────────────────────────────────────────────────

func runHTTP(addr string) {
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	srv := httptransport.NewServer(a, httptransport.ServerOptions{Verbose: true})
	log.Fatal(srv.ListenAndServe(addr))
}

func runTUI(model, thinking, resumeID string) {
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	t := tui.New(a)
	t.SetFlags(model, thinking, resumeID)
	if err := t.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(prompt, model, thinking, output string) {
	if output == "" {
		output = "text"
	}
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts := cli.Opts{Model: model, Thinking: thinking, Output: output}
	if err := cli.Run(ctx, a, prompt, opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runProviders(args []string) {
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunProviders(ctx, a, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runConnect(name, apiKey string) {
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunConnect(ctx, a, name, apiKey, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runDisconnect(name string) {
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunDisconnect(ctx, a, name, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runSessions(all bool) {
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunSessions(ctx, a, all, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runDelete(id string) {
	a := agent.New(agent.AgentOptions{ThinkingLevel: config.GetSettingsManager().ThinkingLevel()})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunDelete(ctx, a, id, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ── Flag parsers ─────────────────────────────────────────────────────────

func extractFlags(args []string) (model, thinking string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--thinking":
			if i+1 < len(args) {
				thinking = args[i+1]
				i++
			}
		}
	}
	return
}

func extractAllFlags(args []string) (model, thinking, resumeID string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--thinking":
			if i+1 < len(args) {
				thinking = args[i+1]
				i++
			}
		case "--resume":
			if i+1 < len(args) {
				resumeID = args[i+1]
				i++
			}
		}
	}
	return
}

func extractPromptFlags(args []string) (model, thinking, output string) {
	output = "text"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--thinking":
			if i+1 < len(args) {
				thinking = args[i+1]
				i++
			}
		case "--output":
			if i+1 < len(args) {
				output = args[i+1]
				i++
			}
		}
	}
	return
}

func printHelp() {
	fmt.Println(`harness — fast terminal agent for coding & conversation

Usage:
  harness                            Interactive TUI mode
  harness -p <prompt> [flags]        Single-turn CLI
  harness http <addr>                HTTP server
  harness --resume <id> [flags]      Resume session in TUI

Management:
  harness providers                  List providers
  harness connect <name>             Connect provider
  harness disconnect <name>          Disconnect provider
  harness sessions                   List sessions (CWD)
  harness sessions --all             List all sessions
  harness delete <id>                Delete session

Flags:
  -p, --prompt <text>  Prompt for single-turn CLI mode
  --model <m>          Model (provider/model)
  --thinking <lvl>     Thinking: off|low|medium|high|xhigh
  --output <mode>      With -p: text|json|json-stream
  --resume <id>        Resume session (TUI only)
  --all                With sessions: list all
  --help, -h           Show this help

Examples:
  harness -p "what is 2+2?"
  harness -p "list files" --output json
  harness -p "hello" --model claude-oauth/opus
  harness --resume abc123 --thinking high
  harness providers
  harness connect anthropic
  harness http :8080`)
}
