package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

const bashTimeout = 30 * time.Second

type bashInput struct {
	Command    string `json:"command"`
	Timeout    int    `json:"timeout,omitempty"`
	Background bool   `json:"background,omitempty"`
}

func Bash() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Bash",
			Description: "Execute a shell command for builds, git, grep/find, installs, and system tasks. Do NOT use it to read, write, or edit files — use the Read, Write, and Edit tools instead. For HTTP requests, use the Fetch tool instead of curl/wget.\n\nTimeout: 30s by default; pass a larger 'timeout' (seconds) for slow commands.\n\nBackground: set 'background' true to run a long-lived process detached from the call. It returns immediately with the PID and a temp log path — stop it with `kill <pid>`, and read the log to check progress.\n\nOutput is truncated to the last 2000 lines or 50KB; when truncated, the full output is saved to a temp file whose path is shown.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "The bash command to execute"},
					"timeout": {"type": "integer", "description": "Timeout in seconds (default: 30). Increase for long-running commands."},
					"background": {"type": "boolean", "description": "If true, run detached in the background: returns immediately with the PID and a temp log path; no timeout applies. Default false."}
				},
				"required": ["command"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args bashInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if args.Background {
				return runBashBackground(args.Command)
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

// runBashBackground launches the command fully detached: a new session (via
// setDetached, per-OS) so it survives the tool-call's process-group teardown,
// with stdout/stderr redirected to a temp log file and stdin from /dev/null.
// It does NOT wait — it returns immediately with the PID and log path so the
// agent can monitor (read the log) or stop it later (kill <pid>). No timeout
// applies. This replaces the fragile "setsid/nohup &" dance callers had to hand-
// roll (setsid(1) doesn't even exist on macOS).
func runBashBackground(command string) (string, error) {
	logFile, err := os.CreateTemp("", "harness-bg-*.log")
	if err != nil {
		return fmt.Sprintf("Error creating log file: %v", err), err
	}
	logPath := logFile.Name()

	cmd := exec.Command("bash", "-c", command)
	if err := setDetached(cmd); err != nil { // new session — escapes the caller's group
		logFile.Close()
		os.Remove(logPath)
		return fmt.Sprintf("Background not available: %v", err), err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Sprintf("Error starting background command: %v", err), err
	}
	pid := cmd.Process.Pid
	// The child holds its own fd to the log; we can close ours. Release the
	// process handle so the Go runtime doesn't try to reap it.
	logFile.Close()
	_ = cmd.Process.Release()

	return fmt.Sprintf("Started in background:\n  PID: %d\n  Log: %s\nStop with: kill %d  ·  Check progress by reading the log.", pid, logPath, pid), nil
}
