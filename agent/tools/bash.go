package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

const bashTimeout = 30 * time.Second

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func Bash() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Bash",
			Description: "Execute a shell command. Use for builds, git, grep/find, installs, and system tasks. Do NOT use for reading, writing, or editing files — use read_file, write_file, and edit instead.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "The bash command to execute"},
					"timeout": {"type": "integer", "description": "Timeout in seconds (default: 30)"}
				},
				"required": ["command"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args bashInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			timeout := bashTimeout
			if args.Timeout > 0 {
				timeout = time.Duration(args.Timeout) * time.Second
			}
			// Combine caller ctx + timeout — whichever fires first
			ctx2, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(ctx2, "bash", "-c", args.Command)
			out, err := cmd.CombinedOutput()
			result := strings.TrimSpace(string(out))
			const maxOutput = 10000
			if len(result) > maxOutput {
				result = result[:maxOutput] + "\n...(truncated)"
			}
			if ctx2.Err() == context.DeadlineExceeded {
				err := fmt.Errorf("timeout after %v", timeout)
				return fmt.Sprintf("Timeout after %v:\n%s", timeout, result), err
			}
			if ctx.Err() != nil {
				return "(stopped)", ctx.Err()
			}
			if err != nil {
				return fmt.Sprintf("Exit error: %v\n%s", err, result), err
			}
			if result == "" {
				return "(no output)", nil
			}
			return result, nil
		},
	}
}
