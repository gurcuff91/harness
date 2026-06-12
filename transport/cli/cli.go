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
	client := newClient(addr)

	// Resolve model from settings if not provided
	model := opts.Model
	thinking := opts.Thinking
	if model == "" || thinking == "" {
		data, err := client.GetSettings()
		if err == nil {
			var settings map[string]string
			json.Unmarshal(data, &settings)
			if model == "" {
				model = settings["active_model"]
			}
			if thinking == "" {
				thinking = settings["thinking_level"]
			}
		}
	}
	if model == "" {
		// Fallback: first available model
		data, err := client.ListModels()
		if err != nil {
			return fmt.Errorf("no model configured and cannot list models: %w", err)
		}
		var models []map[string]any
		json.Unmarshal(data, &models)
		if len(models) == 0 {
			return fmt.Errorf("no models available — connect a provider first")
		}
		model, _ = models[0]["model"].(string)
	}

	// Create session
	cwd, _ := os.Getwd()
	data, err := client.CreateSession(model, cwd)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	var sess map[string]any
	json.Unmarshal(data, &sess)
	sessionID, _ := sess["id"].(string)
	defer client.CloseSession(sessionID) //nolint

	// Open SSE connection BEFORE any commands that trigger events
	ctx2, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	events, err := client.StreamEvents(ctx2, sessionID)
	if err != nil {
		return fmt.Errorf("stream events: %w", err)
	}

	// Apply thinking level (doesn't emit events, safe to do after SSE open)
	if thinking != "" && thinking != "off" {
		client.ExecCommand(sessionID, "thinking", map[string]any{"level": thinking}) //nolint
	}

	// Send prompt — session starts processing, events arrive via SSE
	_, err = client.SendPrompt(sessionID, prompt)
	if err != nil {
		return fmt.Errorf("send prompt: %w", err)
	}

	return renderEvents(events, opts.Output)
}

// renderEvents reads SSE events and renders them according to the output mode.
func renderEvents(events <-chan map[string]any, mode string) error {
	var collected []map[string]any
	var textBuf strings.Builder

	for evt := range events {
		typ, _ := evt["type"].(string)

		// Always collect text deltas for text mode
		if typ == "text" && mode == "text" {
			delta, _ := evt["delta"].(string)
			textBuf.WriteString(delta)
		}

		// Error handling for all modes
		if typ == "error" {
			msg, _ := evt["message"].(string)
			fmt.Fprintln(os.Stderr, "Error:", msg)
			return fmt.Errorf("%s", msg)
		}

		switch mode {
		case "json-stream":
			b, _ := json.Marshal(evt)
			fmt.Println(string(b))
		case "json":
			collected = append(collected, evt)
		}

		if typ == "turn_end" {
			goto finalize
		}
	}
finalize:

	switch mode {
	case "text":
		fmt.Println(strings.TrimSpace(textBuf.String()))
	case "json":
		fmt.Println("[")
		for i, evt := range collected {
			b, _ := json.Marshal(evt)
			if i < len(collected)-1 {
				fmt.Println(string(b) + ",")
			} else {
				fmt.Println(string(b))
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
