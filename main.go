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
	"github.com/gurcuff91/harness/providers"
	httptransport "github.com/gurcuff91/harness/transport/http"
	tuiv2 "github.com/gurcuff91/harness/transport/tui-v2"
)

func main() {
	httpAddr := flag.String("http", "", "Start HTTP server on address (e.g. :8080, 0.0.0.0:3000)")
	flag.Parse()

	model := os.Getenv("HARNESS_MODEL")
	if model == "" {
		model = config.GetSettingsManager().ActiveModel()
	}
	if model == "" {
		providers.EnsureRegistry()
		for _, p := range providers.All {
			if !p.IsActive() {
				continue
			}
			if len(p.Models()) == 0 {
				p.FetchModels()
			}
			if len(p.Models()) > 0 {
				model = p.Name() + "/" + p.Models()[0].ID
				config.GetSettingsManager().SetActiveModel(model)
				break
			}
		}
	}

	a := agent.New(agent.AgentOptions{
		ThinkingLevel: config.GetSettingsManager().ThinkingLevel(),
	})

	if *httpAddr != "" {
		srv := httptransport.NewServer(a, httptransport.ServerOptions{Verbose: true})
		log.Fatal(srv.ListenAndServe(*httpAddr))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	t := tuiv2.New(a, model)
	if err := t.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
