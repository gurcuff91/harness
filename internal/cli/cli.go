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

	// Open SSE connection BEFORE any commands that trigger events
	ctx2, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	events, err := c.StreamEvents(ctx2, sessionID)
	if err != nil {
		return fmt.Errorf("stream events: %w", err)
	}

	// Apply thinking level (doesn't emit events, safe to do after SSE open)
	if thinking != "" && thinking != "off" {
		c.ExecCommand(sessionID, "thinking", map[string]any{"level": thinking}) //nolint
	}

	// Send prompt — session starts processing, events arrive via SSE
	_, err = c.SendPrompt(sessionID, prompt)
	if err != nil {
		return fmt.Errorf("send prompt: %w", err)
	}

	return renderEvents(events, opts.Output)
}

// renderEvents reads SSE events and renders them according to the output mode.
// The json / json-stream modes emit each event's Raw payload verbatim (exactly
// what the server sent — see client.Event.Raw), so machine consumers see the
// canonical wire shape, not a lossy re-encode of the typed struct.
func renderEvents(events <-chan client.Event, mode string) error {
	var collected []json.RawMessage
	var textBuf strings.Builder

	for evt := range events {
		// Always collect text deltas for text mode
		if evt.Type == "text" && mode == "text" {
			textBuf.WriteString(evt.Delta)
		}

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

	switch mode {
	case "text":
		fmt.Println(strings.TrimSpace(textBuf.String()))
	case "json":
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
