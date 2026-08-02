package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

// SubagentExecutor is the closure the Agent builds and passes to the Subagent tool.
// It encapsulates all sub-agent creation logic — the tool itself is stateless.
type SubagentExecutor func(ctx context.Context, prompt string) (string, error)

// subagentInput is the JSON input schema for the Subagent tool.
type subagentInput struct {
	Prompt     string `json:"prompt"`
	Timeout    int    `json:"timeout,omitempty"`
	Background bool   `json:"background,omitempty"`
}

// subagentTimeout is the default wait when Timeout isn't specified — same
// value the tool used unconditionally before it became configurable.
const subagentTimeout = 120 * time.Second

// Subagent returns a Tool that delegates a task to a sub-agent, blocking for
// the response (or, with background: true, returning immediately with a
// path to a result file) — the same timeout/background shape as
// ColleagueAsk, for the same reason: a long-running exploration task
// shouldn't be forced to fit inside one fixed timeout, and shouldn't have to
// block the caller's turn if the model doesn't need the answer immediately.
//
// Unlike ColleagueAsk (which delegates to a separate harness PROCESS),
// Subagent's background mode only survives as long as THIS process does —
// the sub-agent runs in-process via the executor closure, so if the parent
// process exits, the goroutine (and whatever it was about to write to the
// result file) goes with it. "background" here means "outlives the turn
// that launched it", not "survives the process" — worth remembering since
// the name mirrors ColleagueAsk's, but the durability guarantee doesn't. See
// runSubagentBackground's doc comment for why it deliberately does NOT use
// the ctx this tool is invoked with for that goroutine — using it was a real
// bug (every background call died with "context canceled" the instant its
// own turn finished). The intended usage is: turn 1 launches it and moves
// on, a LATER turn reads the result file to see what came of it.
//
// The executor closure is built by the Agent in buildSessionTools — it captures
// cwd, model, and all parent settings. The tool has no knowledge of Agent internals.
func Subagent(executor SubagentExecutor) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSubagent,
			Description: `Spawn an autonomous sub-agent for a self-contained task. PREFER over doing it yourself when: exploring/reading large codebases (keeps your context clean), fetching multiple URLs, analyzing multiple files, or refactoring isolated modules. Invoke MULTIPLE simultaneously — they run in parallel. Each has full tool access. DO NOT use when tasks depend on each other's output. Blocks until the sub-agent finishes and returns its final text — set 'background: true' to get a result-file path immediately instead of waiting, if the task might take a while (background has no timeout: use it for genuinely slow tasks instead of passing a large 'timeout').`,
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"prompt": {"type": "string", "description": "The complete task or question for the sub-agent."},
					"timeout": {"type": "integer", "description": "Seconds to wait for a response (default: 120). Ignored when background is true — background waits as long as needed."},
					"background": {"type": "boolean", "description": "If true, return immediately with a path to a result file instead of blocking, and wait as long as needed (no timeout). Default false."}
				},
				"required": ["prompt"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var req subagentInput
			if err := json.Unmarshal(input, &req); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if strings.TrimSpace(req.Prompt) == "" {
				err := fmt.Errorf("subagent: prompt is required")
				return err.Error(), err
			}

			// Timeout only applies to the blocking (foreground) path — it
			// exists to protect the CALLER from waiting indefinitely. In
			// background mode nothing is waiting: the tool already returned,
			// and the goroutine below is the only thing watching the request.
			// Applying the same timeout there would silently cut off exactly
			// the slow task background was meant to tolerate (see the same
			// reasoning in ColleagueAsk).
			if req.Background {
				return runSubagentBackground(executor, req.Prompt)
			}

			timeout := subagentTimeout
			if req.Timeout > 0 {
				timeout = time.Duration(req.Timeout) * time.Second
			}
			// Combine caller ctx (Stop cancellation) + timeout.
			ctx2, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return executor(ctx2, req.Prompt)
		},
	}
}

// runSubagentBackground runs the executor in a goroutine and writes the
// result to a temp file, returning immediately with that path.
//
// Takes no ctx from the caller on purpose — it always starts the goroutine
// with a fresh context.Background(), never Execute's own. That ctx traces
// back to agent/session.go's drainFollowUps: every turn gets a fresh
// context.WithCancel(parentCtx), and drainFollowUps calls that cancel()
// UNCONDITIONALLY the moment the turn's own promptSync returns — with zero
// awareness of (or waiting for) any goroutine a tool call might have kicked
// off in the background. Using the caller's ctx here was the actual bug:
// the whole point of background mode is "outlive the turn that launched
// it, so a LATER turn can read the result file" — but a ctx tied to that
// same turn is cancelled at the exact moment the turn ends, which is
// earlier than the sub-agent's own work is usually done. The result was
// every background sub-agent call failing with "context canceled" the
// instant its launching turn finished, regardless of transport (TUI,
// Telegram, Slack, ACP, the HTTP /ask or /prompt endpoints — the
// cancellation happens deep in agent/session.go, before any of that even
// gets involved). context.Background() has no relationship to any turn's
// lifecycle, so it can't be cancelled out from under the sub-agent by one
// finishing — which is exactly the "let it run, check back later" model
// this mode exists for.
func runSubagentBackground(executor SubagentExecutor, prompt string) (string, error) {
	f, err := os.CreateTemp("", "harness-subagent-*.txt")
	if err != nil {
		return fmt.Sprintf("Error creating result file: %v", err), err
	}
	path := f.Name()
	f.Close()

	go func() {
		text, err := executor(context.Background(), prompt)
		result := text
		if err != nil {
			result = fmt.Sprintf("Error: %v\n\n%s", err, text)
		}
		_ = os.WriteFile(path, []byte(result), 0644)
	}()

	return fmt.Sprintf("Delegated to a sub-agent in background.\nResult will be written to: %s\nRead that file later to see the response.", path), nil
}
