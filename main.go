package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/transport/cli"
	httptransport "github.com/gurcuff91/harness/transport/http"
	"github.com/gurcuff91/harness/transport/tui"
)

func main() {
	// Parse known flags, collecting unknown args as the prompt.
	// Go's flag package stops at first non-flag, so we pre-scan.
	var (
		httpAddr string
		model    string
		thinking string
		output   string
		prompt   string
	)

	// Pre-separate known flags from prompt args
	var promptParts []string
	args := os.Args[1:]
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--http":
			i++
			if i < len(args) {
				httpAddr = args[i]
			}
		case "--model":
			i++
			if i < len(args) {
				model = args[i]
			}
		case "--thinking":
			i++
			if i < len(args) {
				thinking = args[i]
			}
		case "--output":
			i++
			if i < len(args) {
				output = args[i]
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				// Unknown flag — pass to flag package for error
				promptParts = append(promptParts, args[i])
			} else {
				promptParts = append(promptParts, args[i])
			}
		}
		i++
	}

	if output == "" {
		output = "text"
	}

	prompt = strings.Join(promptParts, " ")

	if httpAddr != "" && prompt != "" {
		fmt.Fprintln(os.Stderr, "error: --http and prompt are mutually exclusive")
		os.Exit(1)
	}

	a := agent.New(agent.AgentOptions{
		ThinkingLevel: config.GetSettingsManager().ThinkingLevel(),
	})

	if httpAddr != "" {
		srv := httptransport.NewServer(a, httptransport.ServerOptions{Verbose: true})
		log.Fatal(srv.ListenAndServe(httpAddr))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if prompt != "" {
		opts := cli.Opts{
			Model:    model,
			Thinking: thinking,
			Output:   output,
		}
		if err := cli.Run(ctx, a, prompt, opts); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	t := tui.New(a)
	if err := t.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
