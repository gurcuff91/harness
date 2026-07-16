package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/internal/transport/cli"
	httptransport "github.com/gurcuff91/harness/internal/transport/http"
	"github.com/gurcuff91/harness/internal/transport/tui"
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

	case "settings":
		runSettings(args[1:])

	case "mcp":
		runMCP(args[1:])

	case "memo":
		runMemo(args[1:])

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
	a := newRootAgent()
	defer a.Close()
	srv := httptransport.NewServer(a, httptransport.ServerOptions{Verbose: true})
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	log.Fatal(srv.Serve(listener))
}

func runTUI(model, thinking, resumeID string) {
	a := newRootAgent()
	defer a.Close()
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
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts := cli.Opts{Model: model, Thinking: thinking, Output: output}
	if err := cli.Run(ctx, a, prompt, opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runProviders(args []string) {
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunProviders(ctx, a, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runConnect(name, apiKey string) {
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunConnect(ctx, a, name, apiKey, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runDisconnect(name string) {
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunDisconnect(ctx, a, name, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runSessions(all bool) {
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunSessions(ctx, a, all, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runDelete(id string) {
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := cli.RunDelete(ctx, a, id, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ── settings / mcp commands ──────────────────────────────────────────────

// newRootAgent builds the process's root agent. EnableMCPs spawns the configured
// MCP servers (once) and registers their tools; the caller must Close() it to
// terminate those subprocesses. It also opens the shared, project-scoped memory
// store (best-effort: if it can't open, the memory tools are simply absent).
func newRootAgent() *agent.Agent {
	var mem *memory.Store
	if s, err := memory.Open(""); err == nil {
		mem = s
	}
	// ThinkingLevel is left zero — agent.New resolves it from settings (then "off").
	return agent.New(agent.AgentOptions{
		EnableMCPs: true,
		Memory:     mem, // *memory.Store directly; the agent wraps it in a scoped adapter
	})
}

func runSettings(args []string) {
	// Settings commands only read/write config — no need to spawn MCP servers.
	a := agent.New(agent.AgentOptions{})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	switch {
	case len(args) == 0:
		err = cli.RunSettings(ctx, a, "text")
	case args[0] == "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: harness settings set <model|thinking> <value>")
			os.Exit(1)
		}
		err = cli.RunSettingsSet(ctx, a, args[1], args[2], "text")
	default:
		fmt.Fprintf(os.Stderr, "unknown settings subcommand: %s\nusage: harness settings [set <key> <value>]\n", args[0])
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runMCP(args []string) {
	// mcp list reports real connection status, so spawn the servers (root agent).
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	switch {
	case len(args) == 0, args[0] == "list":
		err = cli.RunMCPList(ctx, a, "text")
	case args[0] == "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness mcp add <name> [--local|--remote] [flags]")
			os.Exit(1)
		}
		name, opts := parseMCPAddFlags(args[1:])
		err = cli.RunMCPAdd(ctx, a, name, opts, "text")
	case args[0] == "rm", args[0] == "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: harness mcp rm <name>")
			os.Exit(1)
		}
		err = cli.RunMCPRemove(ctx, a, args[1], "text")
	default:
		fmt.Fprintf(os.Stderr, "unknown mcp subcommand: %s\nusage: harness mcp [list | add <name> ... | rm <name>]\n", args[0])
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runMemo dispatches `harness memo [<query>] [--all] [--content] [--limit N] [--skip N]`.
// Read-only: with no query it lists memories; with a query it full-text searches
// them — for the current directory, or across all projects with --all.
func runMemo(args []string) {
	a := newRootAgent()
	defer a.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts := parseMemoFlags(args)
	if err := cli.RunMemo(ctx, a, opts, "text"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// parseMemoFlags parses `memo` args: an optional bare query (no query = list),
// plus --all, --content, --limit, --skip.
func parseMemoFlags(args []string) cli.MemoOpts {
	opts := cli.MemoOpts{Limit: 10}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			opts.All = true
		case "--global":
			opts.Global = true
		case "--content":
			opts.Content = true
		case "--limit":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &opts.Limit)
				i++
			}
		case "--skip":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &opts.Skip)
				i++
			}
		default:
			// A bare non-flag arg is the query (no query → list mode).
			if opts.Query == "" && len(args[i]) > 0 && args[i][0] != '-' {
				opts.Query = args[i]
			}
		}
	}
	return opts
}

// parseMCPAddFlags parses `mcp add` flags. The first non-flag arg is the server
// name. Supports --local/--remote, --command, --url, --bearer, --disabled, and
// repeatable --env KEY=VAL / --header KEY:VAL.
func parseMCPAddFlags(args []string) (name string, opts cli.MCPAddOpts) {
	opts.Env = map[string]string{}
	opts.Headers = map[string]string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--local":
			opts.Local = true
		case "--remote":
			opts.Remote = true
		case "--disabled":
			opts.Disabled = true
		case "--command":
			if i+1 < len(args) {
				opts.Command = args[i+1]
				i++
			}
		case "--url":
			if i+1 < len(args) {
				opts.URL = args[i+1]
				i++
			}
		case "--bearer":
			if i+1 < len(args) {
				opts.Bearer = args[i+1]
				i++
			}
		case "--env":
			if i+1 < len(args) {
				if k, v, ok := splitKV(args[i+1], "="); ok {
					opts.Env[k] = v
				}
				i++
			}
		case "--header":
			if i+1 < len(args) {
				if k, v, ok := splitKV(args[i+1], ":"); ok {
					opts.Headers[k] = v
				}
				i++
			}
		default:
			if name == "" && len(args[i]) > 0 && args[i][0] != '-' {
				name = args[i]
			}
		}
	}
	if len(opts.Env) == 0 {
		opts.Env = nil
	}
	if len(opts.Headers) == 0 {
		opts.Headers = nil
	}
	return name, opts
}

// splitKV splits "key<sep>value" on the first separator. Value may itself
// contain the separator (e.g. a header value with a colon).
func splitKV(s, sep string) (key, value string, ok bool) {
	idx := strings.Index(s, sep)
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+len(sep):]), true
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
  harness connect <name> [api_key]   Connect provider (api_key optional)
  harness disconnect <name>          Disconnect provider
  harness sessions [--all]           List sessions (CWD, or all)
  harness delete <id>                Delete session

Settings:
  harness settings                   Show core settings
  harness settings set <key> <val>   Set: key ∈ {model, thinking}
  harness mcp [list]                 List MCP servers
  harness mcp add <name> [flags]     Add MCP server (see 'mcp add' flags)
  harness mcp rm <name>              Remove MCP server

Memory (read-only — the agent writes memories via its tools):
  harness memo                       List memories (this project + globals)
  harness memo <query>               Full-text search memories
  harness memo <query> --all         Search across ALL projects
  harness memo --global              List only global (cross-project) memories

Flags (CLI / TUI):
  -p, --prompt <text>  Prompt for single-turn CLI mode
  --model <m>          Model (provider/model)
  --thinking <lvl>     Thinking: off|low|medium|high|xhigh
  --output <mode>      With -p: text|json|json-stream
  --resume <id>        Resume session (TUI only)
  --all                With sessions: list all
  --help, -h           Show this help

Flags ('mcp add'):
  --local              Local server (spawns a command)
  --remote             Remote server (dials a URL)
  --command <cmd>      Local: command + args, e.g. "npx -y @mcp/fs"
  --url <url>          Remote: server URL
  --bearer <token>     Remote: sugar for --header "Authorization: Bearer <token>"
  --env KEY=VAL        Local: env var (repeatable)
  --header KEY:VAL     Remote: HTTP header (repeatable)
  --disabled           Add the server disabled (default: enabled)

Flags ('memo'):
  --all                Include memories from ALL projects (not just this one)
  --global             Only global (cross-project) memories
  --content            Show each memory's content preview
  --limit <n>          Max results per page (default 10)
  --skip <n>           Pagination offset (default 0)

Examples:
  harness -p "what is 2+2?"
  harness -p "list files" --output json
  harness -p "hello" --model claude-oauth/opus
  harness --resume abc123 --thinking high
  harness providers
  harness connect anthropic
  harness settings set thinking high
  harness mcp add fs --local --command "npx -y @mcp/fs"
  harness mcp add api --remote --url https://mcp.x --header "Authorization: Bearer t"
  harness memo
  harness memo "deploy process" --content
  harness memo kubernetes --all
  harness http :8080`)
}
