package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeHTTPServer is a minimal Streamable-HTTP MCP server for tests. jsonMode
// controls whether it replies with a direct JSON object or an SSE stream. It
// records the last Authorization header and echoes a session id.
type fakeHTTPServer struct {
	jsonMode  bool
	lastAuth  string
	sessionID string
}

func (f *fakeHTTPServer) handler(w http.ResponseWriter, r *http.Request) {
	f.lastAuth = r.Header.Get("Authorization")

	body, _ := io.ReadAll(r.Body)
	var req request
	json.Unmarshal(body, &req)

	// Notifications (no id) → 202 Accepted, no body.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Build the JSON-RPC result for the method.
	resp := response{JSONRPC: jsonrpcVersion, ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result, _ = json.Marshal(initializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities:    map[string]any{"tools": map[string]any{}},
			ServerInfo:      implementation{Name: "fake-http", Version: "1.0"},
		})
		if f.sessionID != "" {
			w.Header().Set("Mcp-Session-Id", f.sessionID)
		}
	case "tools/list":
		resp.Result, _ = json.Marshal(listToolsResult{Tools: []Tool{
			{Name: "remote_echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}})
	case "tools/call":
		var p callToolParams
		json.Unmarshal(req.Params, &p)
		resp.Result, _ = json.Marshal(CallResult{Content: []contentBlock{{Type: "text", Text: "remote:" + p.Name}}})
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found"}
	}

	respBytes, _ := json.Marshal(resp)

	if f.jsonMode {
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
		return
	}

	// SSE mode: emit a noise notification first, then the real response, to
	// prove the parser skips intermediate events and picks the matching id.
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	noise, _ := json.Marshal(response{JSONRPC: jsonrpcVersion}) // no id → ignored
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", noise)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", respBytes)
}

func newHTTPClient(t *testing.T, jsonMode bool) (*Client, *fakeHTTPServer) {
	t.Helper()
	fake := &fakeHTTPServer{jsonMode: jsonMode, sessionID: "sess-123"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)

	tr, err := NewHTTPTransport(HTTPConfig{
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer test-token"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("new http transport: %v", err)
	}
	c := NewClient(tr)
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, fake
}

func TestHTTPJSONMode(t *testing.T) {
	c, fake := newHTTPClient(t, true)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	res, err := c.CallTool(context.Background(), "remote_echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := res.Text(); got != "remote:remote_echo" {
		t.Errorf("Text() = %q", got)
	}
	// Auth header must have reached the server.
	if fake.lastAuth != "Bearer test-token" {
		t.Errorf("auth header not sent: %q", fake.lastAuth)
	}
}

func TestHTTPSSEMode(t *testing.T) {
	c, _ := newHTTPClient(t, false)
	// SSE path: the response comes embedded in the stream after a noise event.
	res, err := c.CallTool(context.Background(), "remote_echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call (sse): %v", err)
	}
	if got := res.Text(); got != "remote:remote_echo" {
		t.Errorf("Text() = %q, want remote:remote_echo", got)
	}
}

func TestHTTPSessionIDEcho(t *testing.T) {
	// Verify the client captures the server's Mcp-Session-Id and resends it.
	var seenSession string
	fake := &fakeHTTPServer{jsonMode: true, sessionID: "sess-xyz"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Session-Id") != "" {
			seenSession = r.Header.Get("Mcp-Session-Id")
		}
		fake.handler(w, r)
	}))
	defer srv.Close()

	tr, _ := NewHTTPTransport(HTTPConfig{URL: srv.URL})
	c := NewClient(tr)
	defer c.Close()
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	// This second call should carry the session id captured at initialize.
	c.ListTools(context.Background())
	if seenSession != "sess-xyz" {
		t.Errorf("session id not echoed: got %q", seenSession)
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`unauthorized`))
	}))
	defer srv.Close()

	tr, _ := NewHTTPTransport(HTTPConfig{URL: srv.URL})
	c := NewClient(tr)
	defer c.Close()
	err := c.Initialize(context.Background())
	if err == nil {
		t.Fatalf("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status: %v", err)
	}
}
