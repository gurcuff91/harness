package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// StdioTransport speaks JSON-RPC 2.0 to a local MCP server subprocess over its
// stdin/stdout, one JSON object per line. It multiplexes concurrent requests by
// id: a background reader goroutine dispatches each response to the waiting
// caller via a per-id channel.
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	pending map[int64]chan response
	writeMu sync.Mutex // serializes writes to stdin
	closed  bool
	readErr error
}

// StdioConfig configures a local MCP server subprocess.
type StdioConfig struct {
	Command []string          // command + args, e.g. ["npx","-y","@mcp/fs"]
	Env     map[string]string // extra env vars (merged onto os.Environ)
	Cwd     string            // working directory (optional)
}

// NewStdioTransport spawns the server process and starts the reader loop. The
// caller must Close() to terminate the process.
func NewStdioTransport(cfg StdioConfig) (*StdioTransport, error) {
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("mcp stdio: empty command")
	}
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...) //nolint:gosec // user-configured MCP server
	cmd.Dir = cfg.Cwd
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// MCP servers keep stdout clean for JSON-RPC and emit their own logs on
	// stderr (e.g. "Starting default (STDIO) server..."). We DISCARD that stderr
	// so it never contaminates our stdout/TUI. cmd.Stderr = nil sends it to
	// /dev/null.
	cmd.Stderr = nil

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp stdio: start %q: %w", cfg.Command[0], err)
	}

	t := &StdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1<<20), // 1MB lines (tool schemas can be large)
		pending: make(map[int64]chan response),
	}
	go t.readLoop()
	return t, nil
}

// readLoop reads newline-delimited JSON responses and dispatches them by id.
func (t *StdioTransport) readLoop() {
	for {
		line, err := t.stdout.ReadBytes('\n')
		if len(line) > 0 {
			var resp response
			if jsonErr := json.Unmarshal(line, &resp); jsonErr == nil && resp.ID != nil {
				t.mu.Lock()
				ch, ok := t.pending[*resp.ID]
				if ok {
					delete(t.pending, *resp.ID)
				}
				t.mu.Unlock()
				if ok {
					ch <- resp
				}
			}
			// Lines without an id (notifications from server) are ignored in
			// phase 1 — we don't subscribe to server-initiated messages yet.
		}
		if err != nil {
			t.mu.Lock()
			t.readErr = err
			// Fail any in-flight requests so callers unblock.
			for id, ch := range t.pending {
				close(ch)
				delete(t.pending, id)
			}
			t.mu.Unlock()
			return
		}
	}
}

// Send writes a request and waits for its response (or ctx cancellation).
func (t *StdioTransport) Send(ctx context.Context, req request) (response, error) {
	if req.ID == nil {
		return response{}, fmt.Errorf("mcp stdio: Send requires a request id")
	}
	ch := make(chan response, 1)

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return response{}, fmt.Errorf("mcp stdio: transport closed")
	}
	t.pending[*req.ID] = ch
	t.mu.Unlock()

	if err := t.write(req); err != nil {
		t.mu.Lock()
		delete(t.pending, *req.ID)
		t.mu.Unlock()
		return response{}, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, *req.ID)
		t.mu.Unlock()
		return response{}, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return response{}, fmt.Errorf("mcp stdio: connection closed: %w", t.readErr)
		}
		return resp, nil
	}
}

// Notify writes a one-way notification (no id, no waiting).
func (t *StdioTransport) Notify(ctx context.Context, note request) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("mcp stdio: transport closed")
	}
	t.mu.Unlock()
	return t.write(note)
}

// write marshals a message and writes it as one line. Serialized via writeMu.
func (t *StdioTransport) write(msg request) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("mcp stdio: marshal: %w", err)
	}
	b = append(b, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(b); err != nil {
		return fmt.Errorf("mcp stdio: write: %w", err)
	}
	return nil
}

// Close terminates the subprocess and releases resources. Idempotent.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}
