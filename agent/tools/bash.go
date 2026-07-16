package tools

import (
	"bytes"
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
			Description: "Execute a shell command. Use for builds, git, grep/find, installs, and system tasks. Do NOT use for reading, writing, or editing files — use read_file, write_file, and edit instead. The command runs with a default 30s timeout; pass a larger 'timeout' (seconds) for long-running work. To run a process in the background that outlives the call, redirect its output to a file and detach it, e.g. `setsid mycmd > out.log 2>&1 < /dev/null &` — otherwise it holds the output pipe and blocks until the timeout. Output is truncated to the last 2000 lines or 50KB; if truncated, the full output is saved to a temp file whose path is shown (read it for more).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "The bash command to execute"},
					"timeout": {"type": "integer", "description": "Timeout in seconds (default: 30). Increase for long-running commands."}
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

			cmd := exec.Command("bash", "-c", args.Command)
			// Run in its own process group so a timeout can kill the WHOLE tree,
			// not just the direct `bash` child. Without this, background jobs
			// (`cmd &`, nohup) survive, keep the output pipe open, and make the
			// wait block far past the timeout (see setProcessGroup per-OS).
			setProcessGroup(cmd)

			var buf bytes.Buffer
			cmd.Stdout = &buf
			cmd.Stderr = &buf

			start := time.Now()
			if err := cmd.Start(); err != nil {
				return fmt.Sprintf("Error starting command: %v", err), err
			}

			// Wait in a goroutine so we can race it against the timeout / ctx.
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			var (
				runErr    error
				timedOut  bool
				cancelled bool
			)
			select {
			case runErr = <-done:
				// Completed on its own.
			case <-time.After(timeout):
				timedOut = true
				killProcessGroup(cmd) // kill the whole tree, then reap
				<-done
			case <-ctx.Done():
				cancelled = true
				killProcessGroup(cmd)
				<-done
			}
			_ = time.Since(start)

			result := strings.TrimSpace(buf.String())
			result = ApplyTruncation("bash", result, false)

			if timedOut {
				err := fmt.Errorf("timeout after %v", timeout)
				return fmt.Sprintf("Timeout after %v:\n%s", timeout, result), err
			}
			if cancelled {
				return "(stopped)", ctx.Err()
			}
			if runErr != nil {
				return fmt.Sprintf("Exit error: %v\n%s", runErr, result), runErr
			}
			if result == "" {
				return "(no output)", nil
			}
			return result, nil
		},
	}
}
