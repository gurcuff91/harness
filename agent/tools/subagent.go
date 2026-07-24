package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

// SubagentExecutor is the closure the Agent builds and passes to the Subagent tool.
// It encapsulates all sub-agent creation logic — the tool itself is stateless.
type SubagentExecutor func(ctx context.Context, prompt string) (string, error)

// subagentInput is the JSON input schema for the Subagent tool.
type subagentInput struct {
	Prompt string `json:"prompt"`
}

// Subagent returns a Tool that delegates a task to a sub-agent.
// The executor closure is built by the Agent in buildSessionTools — it captures
// cwd, model, and all parent settings. The tool has no knowledge of Agent internals.
func Subagent(executor SubagentExecutor) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSubagent,
			Description: `Spawn an autonomous sub-agent for a self-contained task. PREFER over doing it yourself when: exploring/reading large codebases (keeps your context clean), fetching multiple URLs, analyzing multiple files, or refactoring isolated modules. Invoke MULTIPLE simultaneously — they run in parallel. Each has full tool access. DO NOT use when tasks depend on each other's output.`,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"The complete task or question for the sub-agent."}},"required":["prompt"]}`),
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

			// Combine caller ctx (Stop cancellation) + 5min timeout
			ctx2, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			return executor(ctx2, req.Prompt)
		},
	}
}
