package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// slowHTTPServer is a remote MCP server whose tools/call takes `delay` and whose initialize and
// tools/list answer at once (or, with hangInit, never).
func slowHTTPServer(t *testing.T, delay time.Duration, hangInit bool) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		resp := response{JSONRPC: jsonrpcVersion, ID: req.ID}
		switch req.Method {
		case "initialize":
			if hangInit {
				select {
				case <-release:
				case <-r.Context().Done():
				}
				return
			}
			resp.Result, _ = json.Marshal(initializeResult{ProtocolVersion: protocolVersion, ServerInfo: implementation{Name: "slow", Version: "1"}})
		case "tools/list":
			resp.Result, _ = json.Marshal(listToolsResult{Tools: []Tool{{Name: "slow", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
		case "tools/call":
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
			resp.Result, _ = json.Marshal(CallResult{Content: []contentBlock{{Type: "text", Text: "done"}}})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv.URL
}

// slowStdioServer is the fake stdio server with a slow tools/call.
func slowStdioServer(delay time.Duration) types.MCPServer {
	return types.MCPServer{Command: os.Args[0], Env: map[string]string{
		"MCP_FAKE_SERVER": "1", "MCP_FAKE_DELAY_MS": strconv.Itoa(int(delay / time.Millisecond)),
	}}
}

// start connects one server and returns its single slow-capable tool (the first tool).
func start(t *testing.T, cfg types.MCPServer) tools.Tool {
	t.Helper()
	m := NewManager()
	t.Cleanup(func() { m.Close() })
	got := m.Start(context.Background(), map[string]types.MCPServer{"s": cfg})
	if len(got) == 0 {
		t.Fatalf("server did not connect: %+v", m.Statuses())
	}
	return got[0]
}

// transports runs a test body once per transport, each with a tool call that takes `delay`.
func transports(t *testing.T, delay time.Duration, body func(t *testing.T, cfg types.MCPServer)) {
	t.Run("http", func(t *testing.T) { body(t, types.MCPServer{URL: slowHTTPServer(t, delay, false)}) })
	t.Run("stdio", func(t *testing.T) { body(t, slowStdioServer(delay)) })
}

// The bug this guards: the connect timeout (a few seconds, right for connecting) was also the HTTP
// client's timeout for every later call, so any remote tool slower than that was cut off. A call
// slower than "timeout" but within "call_timeout" must succeed, on both transports.
func TestACallMayTakeLongerThanTheConnectTimeout(t *testing.T) {
	transports(t, 900*time.Millisecond, func(t *testing.T, cfg types.MCPServer) {
		cfg.Timeout = 300 // connect budget: far shorter than the call
		tool := start(t, cfg)
		start := time.Now()
		out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil || out == "" {
			t.Fatalf("a %v call with a 300 ms connect timeout: out=%q err=%v, want it to succeed", 900*time.Millisecond, out, err)
		}
		if time.Since(start) < 800*time.Millisecond {
			t.Fatalf("the call returned after %v: the slow server was not exercised", time.Since(start))
		}
	})
}

func TestACallThatOutlastsCallTimeoutFailsAndSaysWhatToRaise(t *testing.T) {
	transports(t, 5*time.Second, func(t *testing.T, cfg types.MCPServer) {
		cfg.CallTimeout = 300
		tool := start(t, cfg)
		begin := time.Now()
		_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
		took := time.Since(begin)
		if err == nil {
			t.Fatal("a call past call_timeout succeeded")
		}
		if took < 250*time.Millisecond || took > 3*time.Second {
			t.Errorf("failed after %v, want about the 300 ms call_timeout", took)
		}
		name := strings.TrimPrefix(tool.Def.Name, "mcp__s__")
		for _, want := range []string{`mcp server "s"`, `"` + name + `"`, "did not answer within 300ms", "call_timeout"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error %q does not wrap context.DeadlineExceeded", err)
		}
	})
}

// A caller that stops the turn is not a timeout: its own error comes back, with no hint to raise a
// setting that had nothing to do with it.
func TestACallerCancellingKeepsItsOwnError(t *testing.T) {
	transports(t, 5*time.Second, func(t *testing.T, cfg types.MCPServer) {
		tool := start(t, cfg)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(200 * time.Millisecond); cancel() }()
		begin := time.Now()
		_, err := tool.Execute(ctx, json.RawMessage(`{}`))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if strings.Contains(err.Error(), "call_timeout") {
			t.Errorf("a cancellation was blamed on call_timeout: %v", err)
		}
		if time.Since(begin) > 3*time.Second {
			t.Errorf("cancel took %v to end the call", time.Since(begin))
		}
	})
}

// A caller whose own deadline is shorter than call_timeout keeps ITS error, not ours.
func TestACallersOwnShorterDeadlineIsNotBlamedOnCallTimeout(t *testing.T) {
	transports(t, 5*time.Second, func(t *testing.T, cfg types.MCPServer) {
		tool := start(t, cfg) // default call_timeout: 30 min
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, err := tool.Execute(ctx, json.RawMessage(`{}`))
		if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "call_timeout") {
			t.Fatalf("err = %v, want the caller's own deadline, with no call_timeout hint", err)
		}
	})
}

// The connect timeout still bounds connecting.
func TestTheConnectTimeoutStillBoundsConnecting(t *testing.T) {
	m := NewManager()
	defer m.Close()
	begin := time.Now()
	got := m.Start(context.Background(), map[string]types.MCPServer{
		"hung": {URL: slowHTTPServer(t, 0, true), Timeout: 300},
	})
	if len(got) != 0 {
		t.Fatalf("a server that never answers initialize connected: %v", got)
	}
	if d := time.Since(begin); d < 250*time.Millisecond || d > 3*time.Second {
		t.Errorf("gave up after %v, want about the 300 ms connect timeout", d)
	}
	if st := m.Statuses(); len(st) != 1 || st[0].Connected || st[0].Error == "" {
		t.Errorf("statuses = %+v, want one failed server with its error", st)
	}
}

func TestTimeoutDefaultsAndOverrides(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  types.MCPServer
		conn time.Duration
		call time.Duration
	}{
		{"defaults", types.MCPServer{}, 5 * time.Second, 30 * time.Minute},
		{"connect only", types.MCPServer{Timeout: 700}, 700 * time.Millisecond, 30 * time.Minute},
		{"call only", types.MCPServer{CallTimeout: 45000}, 5 * time.Second, 45 * time.Second},
		{"both", types.MCPServer{Timeout: 1000, CallTimeout: 2000}, time.Second, 2 * time.Second},
		{"negative is the default", types.MCPServer{Timeout: -1, CallTimeout: -1}, 5 * time.Second, 30 * time.Minute},
	} {
		if got := connectTimeout(c.cfg); got != c.conn {
			t.Errorf("%s: connectTimeout = %v, want %v", c.name, got, c.conn)
		}
		if got := callTimeout(c.cfg); got != c.call {
			t.Errorf("%s: callTimeout = %v, want %v", c.name, got, c.call)
		}
	}
}

// The HTTP client's own timeout is a backstop BEHIND the call's deadline: it must outlast it, or it
// would fire first and the error would not say which setting to raise.
func TestTheHTTPClientTimeoutOutlastsTheCallDeadline(t *testing.T) {
	var captured time.Duration
	orig := newHTTPTransport
	newHTTPTransport = func(cfg HTTPConfig) (*HTTPTransport, error) { captured = cfg.Timeout; return orig(cfg) }
	defer func() { newHTTPTransport = orig }()
	cfg := types.MCPServer{URL: slowHTTPServer(t, 0, false), CallTimeout: 4000}
	start(t, cfg)
	if captured <= callTimeout(cfg) {
		t.Errorf("HTTP client timeout %v must be longer than the call deadline %v", captured, callTimeout(cfg))
	}
	if captured > callTimeout(cfg)+5*time.Minute {
		t.Errorf("HTTP client timeout %v is not a tight backstop for %v", captured, callTimeout(cfg))
	}
}

func TestCallTimeoutIsPartOfTheSettingsShape(t *testing.T) {
	var s types.MCPServer
	if err := json.Unmarshal([]byte(`{"url":"http://x/mcp","timeout":1000,"call_timeout":600000}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Timeout != 1000 || s.CallTimeout != 600000 {
		t.Fatalf("decoded %+v", s)
	}
	b, _ := json.Marshal(types.MCPServer{URL: "http://x/mcp"})
	if strings.Contains(string(b), "call_timeout") {
		t.Errorf("an unset call_timeout is written out: %s", b)
	}
	b, _ = json.Marshal(s)
	if !strings.Contains(string(b), `"call_timeout":600000`) {
		t.Errorf("call_timeout lost on the way out: %s", b)
	}
}
