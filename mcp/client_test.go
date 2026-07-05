package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestMain lets this test binary act as a fake MCP server when invoked with the
// MCP_FAKE_SERVER env var. The client tests spawn `go test` re-executing this
// same binary as the subprocess server (a common Go pattern for exec tests).
func TestMain(m *testing.M) {
	if os.Getenv("MCP_FAKE_SERVER") != "" {
		runFakeServer()
		return
	}
	os.Exit(m.Run())
}

// runFakeServer implements a minimal MCP server over stdio: it answers
// initialize, tools/list, and tools/call with canned responses.
func runFakeServer() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			return
		}
		if req.ID == nil {
			continue // notification (e.g. initialized) — no reply
		}
		resp := response{JSONRPC: jsonrpcVersion, ID: req.ID}
		switch req.Method {
		case "initialize":
			resp.Result, _ = json.Marshal(initializeResult{
				ProtocolVersion: protocolVersion,
				Capabilities:    map[string]any{"tools": map[string]any{}},
				ServerInfo:      implementation{Name: "fake", Version: "1.0"},
			})
		case "tools/list":
			resp.Result, _ = json.Marshal(listToolsResult{Tools: []Tool{
				{Name: "echo", Description: "echoes input", InputSchema: json.RawMessage(`{"type":"object"}`)},
				{Name: "add", Description: "adds numbers", InputSchema: json.RawMessage(`{"type":"object"}`)},
			}})
		case "tools/call":
			var p callToolParams
			json.Unmarshal(req.Params, &p)
			resp.Result, _ = json.Marshal(CallResult{
				Content: []contentBlock{{Type: "text", Text: "called:" + p.Name}},
			})
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found"}
		}
		_ = enc.Encode(resp)
	}
}

// newFakeClient spawns this test binary as a fake MCP server and returns a
// connected, initialized client.
func newFakeClient(t *testing.T) *Client {
	t.Helper()
	cmd := []string{os.Args[0]}
	tr, err := NewStdioTransport(StdioConfig{
		Command: cmd,
		Env:     map[string]string{"MCP_FAKE_SERVER": "1"},
	})
	if err != nil {
		t.Fatalf("spawn fake server: %v", err)
	}
	c := NewClient(tr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestInitializeAndListTools(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "echo" || tools[1].Name != "add" {
		t.Errorf("unexpected tools: %+v", tools)
	}
}

func TestCallTool(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()
	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected tool error")
	}
	if got := res.Text(); got != "called:echo" {
		t.Errorf("Text() = %q, want called:echo", got)
	}
}

func TestCallToolNilArgs(t *testing.T) {
	c := newFakeClient(t)
	// Nil args must be sent as {} and not crash the server.
	res, err := c.CallTool(context.Background(), "add", nil)
	if err != nil {
		t.Fatalf("call tool nil args: %v", err)
	}
	if got := res.Text(); got != "called:add" {
		t.Errorf("Text() = %q, want called:add", got)
	}
}

func TestConcurrentCalls(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()
	const n = 10
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := c.CallTool(ctx, "echo", json.RawMessage(`{}`))
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent call %d: %v", i, err)
		}
	}
}

// TestSpawnFailure verifies a bad command surfaces an error (degradation path).
func TestSpawnFailure(t *testing.T) {
	_, err := NewStdioTransport(StdioConfig{Command: []string{"this-binary-does-not-exist-xyz"}})
	if err == nil {
		t.Fatalf("expected spawn error for missing binary")
	}
}

// ensure exec is referenced (used indirectly); keeps imports honest if the
// spawn helper changes.
var _ = exec.Command
