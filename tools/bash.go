package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/gurcuff91/harness/llm"
)

const bashTimeout = 30 * time.Second

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds, default 30
}

// Bash returns a tool that executes shell commands.
func Bash() Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "string",
				"description": "The bash command to execute"
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (default: 30)"
			}
		},
		"required": ["command"]
	}`)

	return Tool{
		Def: llm.ToolDef{
			Name:        "bash",
			Description: "Execute a bash command and return stdout/stderr. Use for running code, installing packages, git operations, searching files (grep/find), and general system tasks. Do NOT use for reading/writing/editing files — use the dedicated file tools instead.",
			InputSchema: schema,
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args bashInput
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			timeout := bashTimeout
			if args.Timeout > 0 {
				timeout = time.Duration(args.Timeout) * time.Second
			}

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
			out, err := cmd.CombinedOutput()

			result := strings.TrimSpace(string(out))

			// Truncate large outputs
			const maxOutput = 10000
			if len(result) > maxOutput {
				result = result[:maxOutput] + "\n...(truncated)"
			}

			if err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return fmt.Sprintf("TIMEOUT after %v:\n%s", timeout, result), fmt.Errorf("timeout")
				}
				return fmt.Sprintf("EXIT ERROR: %v\n%s", err, result), fmt.Errorf("exit error")
			}

			if result == "" {
				return "(no output)", nil
			}
			return result, nil
		},
	}
}
