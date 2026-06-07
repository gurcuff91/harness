package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/config"
	httptransport "github.com/gurcuff91/harness/transport/http"
	"github.com/gurcuff91/harness/transport/tui"
)

func main() {
	httpAddr := flag.String("http", "", "Start HTTP server on address (e.g. :8080)")
	flag.Parse()

	a := agent.New(agent.AgentOptions{
		ThinkingLevel: config.GetSettingsManager().ThinkingLevel(),
	})

	if *httpAddr != "" {
		srv := httptransport.NewServer(a, httptransport.ServerOptions{Verbose: true})
		log.Fatal(srv.ListenAndServe(*httpAddr))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	t := tui.New(a)
	if err := t.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
