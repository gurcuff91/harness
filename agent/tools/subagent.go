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
			Description: "Delegate a self-contained task to a sub-agent that runs autonomously.\nUse for parallel work (fetch multiple URLs, analyze multiple files) or isolated subtasks.\nThe sub-agent has the same tools. Invoke multiple times simultaneously for parallelism.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"The complete task or question for the sub-agent."}},"required":["prompt"]}`),
		},
		Execute: func(input json.RawMessage) (string, error) {
			var req subagentInput
			if err := json.Unmarshal(input, &req); err != nil {
				return "", fmt.Errorf("subagent: invalid input: %w", err)
			}
			if strings.TrimSpace(req.Prompt) == "" {
				return "", fmt.Errorf("subagent: prompt is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			return executor(ctx, req.Prompt)
		},
	}
}
