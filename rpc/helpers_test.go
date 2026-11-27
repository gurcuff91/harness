package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gurcuff91/harness/types"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/internal/jsonrpc"
)

// fakeModel is an OpenAI-dialect server: while the last message is not a tool
// result it answers with calls to the tools in `calls` (all in ONE response, so
// they run concurrently); once the results are in it answers "got:" + them.
type fakeModel struct {
	calls []string
}

func (f *fakeModel) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
		return
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "text/event-stream")
	var results []string
	for i := len(body.Messages) - 1; i >= 0 && body.Messages[i].Role == "tool"; i-- {
		results = append([]string{fmt.Sprint(body.Messages[i].Content)}, results...)
	}
	switch {
	case len(results) > 0:
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%q}}]}`+"\n\n", "got:"+strings.Join(results, "|"))
	case len(f.calls) > 0:
		for i, name := range f.calls {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":%d,"id":"call_%d","type":"function","function":{"name":%q,"arguments":"{\"n\":%d}"}}]}}]}`+"\n\n", i, i, name, i)
		}
	default:
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
	}
	fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// registerModel registers the fake provider and returns its "provider/model".
func registerModel(t *testing.T, f *fakeModel) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(ts.Close)
	prov := "rpc-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	if err := admin.RegisterOpenAI(prov, ts.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider(prov) })
	return prov + "/m"
}

// client is the test's side of the protocol: a jsonrpc.Conn on the pipes Run
// uses, plus the recorded notifications and a settable `tool/call` answerer.
type client struct {
	t      *testing.T
	conn   *jsonrpc.Conn
	inW    *io.PipeWriter // the binary's stdin
	done   chan error     // Run's result
	events chan map[string]any
	cancel chan ToolCancelParams
	served chan struct{}

	mu       sync.Mutex
	onTool   func(ctx context.Context, p ToolCallParams) (ToolCallResult, error)
	srv      *Server
	srvReady chan struct{}
	// ext, when set, answers the methods handle does not know (the client
	// stores' settings/* and credentials/*); handled=false falls through.
	ext func(ctx context.Context, method string, params json.RawMessage) (res any, err error, handled bool)
}

type startOpt func(*runConfig)

// start launches Run on pipes. HOME is private to the test.
func start(t *testing.T, opts ...startOpt) *client {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, inW: inW, done: make(chan error, 1), events: make(chan map[string]any, 4096),
		cancel: make(chan ToolCancelParams, 64), served: make(chan struct{}), srvReady: make(chan struct{})}
	cfg := runConfig{stdin: inR, stdout: outW, logger: newCaptureLogger()}
	for _, o := range opts {
		o(&cfg)
	}
	c.conn = jsonrpc.NewConn(outR, inW)
	go func() {
		defer close(c.served)
		c.conn.Serve(context.Background(), c.handle)
	}()
	go func() {
		c.done <- runWith(context.Background(), cfg, func(s *Server) {
			c.srv = s
			close(c.srvReady)
		})
		outW.Close() // the binary exited: the client sees EOF
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after stdin closed")
		}
		inR.Close()
		outR.Close()
	})
	select {
	case <-c.srvReady:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return c
}

func (c *client) handle(ctx context.Context, method string, params json.RawMessage, _ bool) (any, error) {
	switch method {
	case "event":
		var m map[string]any
		json.Unmarshal(params, &m)
		c.events <- m
		return nil, nil
	case "tool/cancel":
		var p ToolCancelParams
		json.Unmarshal(params, &p)
		c.cancel <- p
		return nil, nil
	case "tool/call":
		var p ToolCallParams
		json.Unmarshal(params, &p)
		c.mu.Lock()
		h := c.onTool
		c.mu.Unlock()
		if h == nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "no tool handler"}
		}
		return h(ctx, p)
	}
	c.mu.Lock()
	ext := c.ext
	c.mu.Unlock()
	if ext != nil {
		if res, err, ok := ext(ctx, method, params); ok {
			return res, err
		}
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: method}
}

func (c *client) setTool(h func(ctx context.Context, p ToolCallParams) (ToolCallResult, error)) {
	c.mu.Lock()
	c.onTool = h
	c.mu.Unlock()
}

// call sends a request and returns the raw result or the *jsonrpc.Error.
func (c *client) call(method string, params any) (json.RawMessage, *jsonrpc.Error) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := c.conn.Call(ctx, method, params)
	if err != nil {
		je, ok := err.(*jsonrpc.Error)
		if !ok {
			c.t.Fatalf("%s: transport error: %v", method, err)
		}
		return nil, je
	}
	return res, nil
}

// must calls and decodes the result into out (nil to ignore it).
func (c *client) must(method string, params, out any) {
	c.t.Helper()
	res, je := c.call(method, params)
	if je != nil {
		c.t.Fatalf("%s: %v", method, je)
	}
	if out != nil {
		if err := json.Unmarshal(res, out); err != nil {
			c.t.Fatalf("%s: decode %s: %v", method, res, err)
		}
	}
}

// fail calls and expects an error with the given code and data.kind.
func (c *client) fail(method string, params any, code int, kind string) *jsonrpc.Error {
	c.t.Helper()
	_, je := c.call(method, params)
	if je == nil {
		c.t.Fatalf("%s: expected error %d, got success", method, code)
	}
	if je.Code != code {
		c.t.Fatalf("%s: code = %d (%s), want %d", method, je.Code, je.Message, code)
	}
	if kind != "" {
		d, _ := je.Data.(map[string]any)
		if d["kind"] != kind {
			c.t.Fatalf("%s: kind = %v, want %s", method, je.Data, kind)
		}
	}
	return je
}

func (c *client) initialize(cfg Config) InitializeResult {
	c.t.Helper()
	var res InitializeResult
	c.must("initialize", InitializeParams{ProtocolVersion: ProtocolVersion, ClientInfo: ClientInfo{Name: "test"}, Config: cfg}, &res)
	return res
}

// waitEvent returns the next event of the given type for session id.
func (c *client) waitEvent(typ string) map[string]any {
	c.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-c.events:
			if e["type"] == typ {
				return e
			}
		case <-timeout:
			c.t.Fatalf("no %q event", typ)
		}
	}
}

// collectUntil gathers events until one of type typ arrives (inclusive).
func (c *client) collectUntil(typ string) []map[string]any {
	c.t.Helper()
	var got []map[string]any
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-c.events:
			got = append(got, e)
			if e["type"] == typ {
				return got
			}
		case <-timeout:
			c.t.Fatalf("no %q event; got %v", typ, got)
		}
	}
}

// memStore is the config most tests use: sessions in memory, no resources.
func memStoreConfig(tools ...ToolDecl) Config {
	return Config{Store: &KindConfig{Kind: "ephemeral"}, Tools: tools}
}

// captureLogger records warnings.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func newCaptureLogger() *captureLogger { return &captureLogger{} }
func (l *captureLogger) add(level, event string, kv []any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprint(level, " ", event, " ", kv))
	l.mu.Unlock()
}
func (l *captureLogger) Info(c, e string, kv ...any)  { l.add("info", e, kv) }
func (l *captureLogger) Warn(c, e string, kv ...any)  { l.add("warn", e, kv) }
func (l *captureLogger) Error(c, e string, kv ...any) { l.add("error", e, kv) }

type bytesBuffer = bytes.Buffer
type imageData = types.ImageData

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

func pipe() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }
