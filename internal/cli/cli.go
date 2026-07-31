// Package cli provides a single-turn prompt transport for harness.
// Usage: harness "what is 2+2?" [--model ...] [--thinking ...] [--output text|json|json-stream]
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/client"
)

// Opts configures the CLI transport.
type Opts struct {
	Model    string // "provider/model" — empty means use settings default
	Thinking string // thinking level — empty means use settings default
	Output   string // "text" | "json" | "json-stream" (default: "text")
}

// Client is the HTTP API interface used by the CLI transport. an internal HTTP server, creates a session, sends a prompt,
// and streams the response to stdout according to the output mode.
func Run(ctx context.Context, a *agent.Agent, prompt string, opts Opts) error {
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}

	// Resolve output mode
	switch opts.Output {
	case "", "text":
		opts.Output = "text"
	case "json", "json-stream":
	default:
		return fmt.Errorf("invalid output mode: %s (use text, json, or json-stream)", opts.Output)
	}

	// Start server + client
	server, addr, err := startInternalServer(a)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	defer server.Close()
	c := newClient(addr)

	// Resolve model from settings if not provided
	model := opts.Model
	thinking := opts.Thinking
	if model == "" || thinking == "" {
		if settings, err := c.GetSettings(); err == nil {
			if model == "" {
				model = settings.ActiveModel
			}
			if thinking == "" {
				thinking = settings.ThinkingLevel
			}
		}
	}
	if model == "" {
		// Fallback: first available model
		models, err := c.ListModels()
		if err != nil {
			return fmt.Errorf("no model configured and cannot list models: %w", err)
		}
		if len(models) == 0 {
			return fmt.Errorf("no models available — connect a provider first")
		}
		model = models[0].Model
	}

	// Create session
	cwd, _ := os.Getwd()
	sess, err := c.CreateSession(model, cwd, "")
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	sessionID := sess.ID
	defer c.CloseSession(sessionID) //nolint

	ctx2, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Apply thinking level before sending the prompt.
	if thinking != "" && thinking != "off" {
		c.ExecCommand(sessionID, "thinking", map[string]any{"level": thinking}) //nolint
	}

	// text mode only needs the final response — Ask (PromptAndWait) is a single
	// blocking request/response, no SSE plumbing required. json/json-stream need
	// the individual events (tool calls, thinking, tokens, timing), so they keep
	// the SendPrompt + StreamEvents path.
	if opts.Output == "text" {
		text, err := c.Ask(sessionID, prompt)
		if err != nil {
			return fmt.Errorf("ask: %w", err)
		}
		fmt.Println(strings.TrimSpace(text))
		return nil
	}

	// Open SSE connection BEFORE sending the prompt so no events are missed.
	events, err := c.StreamEvents(ctx2, sessionID)
	if err != nil {
		return fmt.Errorf("stream events: %w", err)
	}

	// Send prompt — session starts processing, events arrive via SSE
	_, err = c.SendPrompt(sessionID, prompt)
	if err != nil {
		return fmt.Errorf("send prompt: %w", err)
	}

	return renderEvents(events, opts.Output)
}

// renderEvents reads SSE events and renders them according to the output mode
// (json or json-stream — text mode is handled by Ask in Run, never reaches
// here). Both modes emit each event's Raw payload verbatim (exactly what the
// server sent — see client.Event.Raw), so machine consumers see the canonical
// wire shape, not a lossy re-encode of the typed struct.
func renderEvents(events <-chan client.Event, mode string) error {
	var collected []json.RawMessage

	for evt := range events {
		// Error handling for all modes
		if evt.Type == "error" {
			fmt.Fprintln(os.Stderr, "Error:", evt.Message)
			return fmt.Errorf("%s", evt.Message)
		}

		switch mode {
		case "json-stream":
			fmt.Println(string(evt.Raw))
		case "json":
			collected = append(collected, evt.Raw)
		}

		if evt.Type == "turn_end" {
			goto finalize
		}
	}
finalize:

	if mode == "json" {
		fmt.Println("[")
		for i, raw := range collected {
			if i < len(collected)-1 {
				fmt.Println(string(raw) + ",")
			} else {
				fmt.Println(string(raw))
			}
		}
		fmt.Println("]")
	}

	return nil
}

// shortenPath replaces home dir with ~
func shortenPath(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
