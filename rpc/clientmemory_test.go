package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/memory/storetest"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/internal/jsonrpc"
)

// ── the client's side: a memory store and a resource loader it serves ────

// clientMemImpl serves memory/* from a correct store (the reference), with
// hooks to record, fail and hang.
type clientMemImpl struct {
	mu     sync.Mutex
	store  memory.Store
	calls  []string
	params map[string][]json.RawMessage
	fail   map[string]string
	hang   map[string]bool
	// rawSearch, when set, is the verbatim result of memory/search.
	rawSearch string
}

func newClientMemImpl() *clientMemImpl {
	return &clientMemImpl{store: storetest.NewReference(), params: map[string][]json.RawMessage{}, fail: map[string]string{}, hang: map[string]bool{}}
}

func (m *clientMemImpl) count(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c == method {
			n++
		}
	}
	return n
}

func (m *clientMemImpl) lastParams(method string) json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.params[method]
	if len(p) == 0 {
		return nil
	}
	return p[len(p)-1]
}

func (m *clientMemImpl) serve(ctx context.Context, method string, params json.RawMessage) (any, error, bool) {
	if !strings.HasPrefix(method, "memory/") {
		return nil, nil, false
	}
	m.mu.Lock()
	m.calls = append(m.calls, method)
	m.params[method] = append(m.params[method], append(json.RawMessage(nil), params...))
	hang, fail, raw := m.hang[method], m.fail[method], m.rawSearch
	m.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err(), true
	}
	if fail != "" {
		return nil, &jsonrpc.Error{Code: -32000, Message: fail}, true
	}
	switch method {
	case "memory/write":
		var p MemoryWriteParams
		_ = json.Unmarshal(params, &p)
		created, err := m.store.Write(p.CWD, p.Slug, p.Content, p.Global)
		return MemoryWriteResult{Created: created}, err, true
	case "memory/get":
		var p MemoryKeyParams
		_ = json.Unmarshal(params, &p)
		mem, found, err := m.store.Get(p.CWD, p.Slug, p.Global)
		return MemoryGetResult{Found: found, Memory: mem}, err, true
	case "memory/search":
		if raw != "" {
			return json.RawMessage(raw), nil, true
		}
		var p MemorySearchParams
		_ = json.Unmarshal(params, &p)
		r, err := m.store.Search(p.CWD, p.Query, p.IncludeContent, p.Skip, p.Limit)
		return r, err, true
	case "memory/delete":
		var p MemoryKeyParams
		_ = json.Unmarshal(params, &p)
		d, err := m.store.Delete(p.CWD, p.Slug, p.Global)
		return MemoryDeleteResult{Deleted: d}, err, true
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: method}, true
}

// clientResImpl serves resources/load: one Resources per cwd, or a default.
type clientResImpl struct {
	mu   sync.Mutex
	cwds []string
	by   map[string]resources.Resources
	def  resources.Resources
	fail string
	hang bool
	// gate, when set, is called with every request (the concurrency barrier).
	gate func(cwd string)
}

func (r *clientResImpl) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cwds...)
}

func (r *clientResImpl) serve(ctx context.Context, method string, params json.RawMessage) (any, error, bool) {
	if method != "resources/load" {
		return nil, nil, false
	}
	var p ResourcesLoadParams
	_ = json.Unmarshal(params, &p)
	r.mu.Lock()
	r.cwds = append(r.cwds, p.CWD)
	res, ok := r.by[p.CWD]
	if !ok {
		res = r.def
	}
	fail, hang, gate := r.fail, r.hang, r.gate
	r.mu.Unlock()
	if gate != nil {
		gate(p.CWD)
	}
	if hang {
		<-ctx.Done()
		return nil, ctx.Err(), true
	}
	if fail != "" {
		return nil, &jsonrpc.Error{Code: -32000, Message: fail}, true
	}
	return res, nil, true
}

// serveMemRes makes the test client answer memory/* and resources/load.
func (c *client) serveMemRes(m *clientMemImpl, r *clientResImpl) {
	c.mu.Lock()
	c.ext = func(ctx context.Context, method string, params json.RawMessage) (any, error, bool) {
		if m != nil {
			if res, err, ok := m.serve(ctx, method, params); ok {
				return res, err, ok
			}
		}
		if r != nil {
			return r.serve(ctx, method, params)
		}
		return nil, nil, false
	}
	c.mu.Unlock()
}

func clientMemResCfg() Config {
	cfg := memStoreConfig()
	cfg.Memory = &KindConfig{Kind: "client"}
	cfg.Resources = &ResourcesConfig{Kind: "client"}
	return cfg
}

// ── a scripted model: the last user message names the tool calls ─────────

type scriptCall struct {
	name string
	args string
}

// scriptModel answers a user message with the tool calls scripted for its
// text, and, once results are in, with "got:" + the results.
type scriptModel struct {
	script map[string][]scriptCall
}

func (f *scriptModel) handler(w http.ResponseWriter, r *http.Request) {
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
	i := len(body.Messages) - 1
	for ; i >= 0 && body.Messages[i].Role == "tool"; i-- {
		results = append([]string{fmt.Sprint(body.Messages[i].Content)}, results...)
	}
	switch {
	case len(results) > 0:
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%q}}]}`+"\n\n", "got:"+strings.Join(results, "|"))
	default:
		var text string
		if i >= 0 {
			text = fmt.Sprint(body.Messages[i].Content)
		}
		calls := f.script[text]
		if len(calls) == 0 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		}
		for n, c := range calls {
			args, _ := json.Marshal(c.args)
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":%d,"id":"call_%d","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`+"\n\n", n, n, c.name, args)
		}
	}
	fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func registerScript(t *testing.T, f *scriptModel) string {
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

func (c *client) ask(id, text string) string {
	c.t.Helper()
	var r SessionAskResult
	c.must("session/ask", SessionPromptParams{ID: id, Text: text}, &r)
	return r.Text
}

func (c *client) sysprompt(id string) string {
	c.t.Helper()
	var sp SessionSyspromptResult
	c.must("session/sysprompt", SessionIDParams{ID: id}, &sp)
	return sp.Text
}

// connOver connects a handler (the "client") to a binary-side Conn over pipes.
func connOver(t *testing.T, h func(ctx context.Context, method string, p json.RawMessage) (any, error)) *jsonrpc.Conn {
	t.Helper()
	aR, aW := pipe()
	bR, bW := pipe()
	binary := jsonrpc.NewConn(aR, bW)
	cl := jsonrpc.NewConn(bR, aW)
	ctx, cancel := context.WithCancel(context.Background())
	go binary.Serve(ctx, func(context.Context, string, json.RawMessage, bool) (any, error) { return nil, nil })
	go cl.Serve(ctx, func(ctx context.Context, method string, p json.RawMessage, _ bool) (any, error) {
		return h(ctx, method, p)
	})
	t.Cleanup(func() { cancel(); aW.Close(); bW.Close() })
	return binary
}

// ── (a) a full turn through the client's memory ──────────────────────────

func TestClientMemoryFullTurn(t *testing.T) {
	model := registerScript(t, &scriptModel{script: map[string][]scriptCall{
		"remember": {{"MemoWrite", `{"slug":"db-schema","content":"orders table has a unique zebra index"}`}},
		"recall":   {{"MemoSearch", `{"query":"zebra"}`}},
	}})
	c := start(t)
	m := newClientMemImpl()
	c.serveMemRes(m, &clientResImpl{})
	c.initialize(clientMemResCfg())
	cwd := t.TempDir()
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: cwd, Model: model}, &d)
	if !strings.Contains(c.sysprompt(d.ID), "No memories yet") {
		t.Fatal("a fresh memory should say so in the prompt")
	}
	if got := c.ask(d.ID, "remember"); !strings.Contains(got, `Saved memory \"db-schema\"`) && !strings.Contains(got, `Saved memory "db-schema"`) {
		t.Fatalf("write result = %q", got)
	}
	got := c.ask(d.ID, "recall")
	if !strings.Contains(got, "zebra index") {
		t.Fatalf("search result did not come from the client's store: %q", got)
	}
	if m.count("memory/write") != 1 || m.count("memory/search") < 2 {
		t.Fatalf("calls = %v", m.calls)
	}
	var wp MemoryWriteParams
	json.Unmarshal(m.lastParams("memory/write"), &wp)
	if wp.CWD != cwd || wp.Slug != "db-schema" || wp.Global {
		t.Fatalf("write params = %+v", wp)
	}
	// the data lives only in the client's store
	if _, found, _ := m.store.Get(cwd, "db-schema", false); !found {
		t.Fatal("the memory is not in the client's store")
	}

	// reopening the session builds the prompt again: the slug list comes from the client
	c.must("session/close", SessionIDParams{ID: d.ID}, nil)
	var again agent.SessionDetail
	c.must("session/resume", SessionResumeParams{ID: d.ID}, &again)
	sp := c.sysprompt(again.ID)
	if !strings.Contains(sp, "Your memories:\n- db-schema") {
		t.Fatalf("the memory list is not in the prompt:\n%s", sp[max(0, len(sp)-600):])
	}
}

// ── (c) errors and timeouts ──────────────────────────────────────────────

func TestClientMemoryFailingSearchDoesNotStopASessionOpening(t *testing.T) {
	for _, mode := range []string{"error", "hang"} {
		t.Run(mode, func(t *testing.T) {
			shortTimeout(t, 150*time.Millisecond)
			model := registerScript(t, &scriptModel{script: map[string][]scriptCall{
				"remember": {{"MemoWrite", `{"slug":"x","content":"y"}`}},
			}})
			c := start(t)
			m := newClientMemImpl()
			if mode == "error" {
				m.fail["memory/search"] = "index is down"
				m.fail["memory/write"] = "disk full"
			} else {
				m.hang["memory/search"] = true
				m.hang["memory/write"] = true
			}
			c.serveMemRes(m, &clientResImpl{})
			c.initialize(clientMemResCfg())
			var d agent.SessionDetail
			c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
			sp := c.sysprompt(d.ID)
			if !strings.Contains(sp, "## Memory") || strings.Contains(sp, "Your memories") || strings.Contains(sp, "No memories yet") {
				t.Fatalf("a failing search must leave the list out:\n%s", sp[max(0, len(sp)-500):])
			}
			got := c.ask(d.ID, "remember")
			want := "disk full"
			if mode == "hang" {
				want = "the client did not answer memory/write within 150ms"
			}
			if !strings.Contains(got, want) {
				t.Fatalf("the model should receive %q, got %q", want, got)
			}
		})
	}
}

func TestClientMemoryErrorWording(t *testing.T) {
	shortTimeout(t, 120*time.Millisecond)
	m := newClientMemImpl()
	m.fail["memory/get"] = "boom"
	m.hang["memory/delete"] = true
	log := newCaptureLogger()
	mem := clientMemory{conn: connOver(t, func(ctx context.Context, method string, p json.RawMessage) (any, error) {
		res, err, _ := m.serve(ctx, method, p)
		return res, err
	}), timeout: clientStoreTimeout, logger: log}
	_, _, getErr := mem.Get("/p", "s", false)
	if getErr == nil || !strings.Contains(getErr.Error(), "memory/get: client error -32000: boom") {
		t.Fatalf("get err = %v", getErr)
	}
	_, delErr := mem.Delete("/p", "s", false)
	if delErr == nil || !strings.Contains(delErr.Error(), "the client did not answer memory/delete within 120ms") {
		t.Fatalf("delete err = %v", delErr)
	}
	// Both are a failure of the client's backend: an upstream error, not an internal one.
	for _, e := range []error{getErr, delErr} {
		if !errors.Is(e, ErrClientBackend) {
			t.Errorf("%v does not wrap ErrClientBackend", e)
		}
		if je := rpcError(e); je == nil || je.Code != CodeUpstream {
			t.Errorf("%v maps to %v, want the upstream code", e, je)
		}
	}
	if len(log.lines) != 2 || !strings.Contains(log.lines[1], "memory/delete failed") {
		t.Fatalf("log = %v", log.lines)
	}
}

// ── adapter details: params on the wire, nil results ─────────────────────

func TestClientMemorySendsScopeFlagsAndNormalisesNilResults(t *testing.T) {
	m := newClientMemImpl()
	mem := clientMemory{conn: connOver(t, func(ctx context.Context, method string, p json.RawMessage) (any, error) {
		res, err, _ := m.serve(ctx, method, p)
		return res, err
	}), logger: newCaptureLogger()}

	if _, err := mem.Write("/p", "g", "text", true); err != nil {
		t.Fatal(err)
	}
	var wp MemoryWriteParams
	json.Unmarshal(m.lastParams("memory/write"), &wp)
	if !wp.Global || wp.CWD != "/p" || wp.Slug != "g" || wp.Content != "text" {
		t.Fatalf("write params = %+v", wp)
	}
	if _, found, err := mem.Get("/p", "g", true); err != nil || !found {
		t.Fatalf("global get = %v %v", found, err)
	}
	if _, found, _ := mem.Get("/p", "g", false); found {
		t.Fatal("a global memory must not answer a project get")
	}
	var kp MemoryKeyParams
	json.Unmarshal(m.lastParams("memory/get"), &kp)
	if kp.Global {
		t.Fatalf("get params = %+v", kp)
	}
	if d, err := mem.Delete("/p", "g", true); err != nil || !d {
		t.Fatalf("global delete = %v %v", d, err)
	}
	json.Unmarshal(m.lastParams("memory/delete"), &kp)
	if !kp.Global {
		t.Fatalf("delete params = %+v", kp)
	}

	m.mu.Lock()
	m.rawSearch = `{"total":0,"returned":0,"skip":0,"limit":10,"results":null}`
	m.mu.Unlock()
	r, err := mem.Search("/p", "", false, 0, 0)
	if err != nil || r.Results == nil || len(r.Results) != 0 {
		t.Fatalf("a null results must become []: %#v %v", r, err)
	}
	m.mu.Lock()
	m.rawSearch = `{"total":0}`
	m.mu.Unlock()
	if r, _ := mem.Search("/p", "", false, 0, 0); r.Results == nil {
		t.Fatal("an absent results must become []")
	}
	if err := mem.Close(); err != nil {
		t.Fatal(err)
	}
}

// ── (d) configuration ────────────────────────────────────────────────────

func TestMemoryAndResourcesConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(*Config)
		want string // "" = accepted
	}{
		{"memory client", func(c *Config) { c.Memory = &KindConfig{Kind: "client"} }, ""},
		{"memory sqlite", func(c *Config) { c.Memory = &KindConfig{Kind: "sqlite", Path: "{dir}/m.db"} }, ""},
		{"memory client with path", func(c *Config) { c.Memory = &KindConfig{Kind: "client", Path: "/x"} }, `config.memory.path is not accepted by kind "client"`},
		{"memory unknown", func(c *Config) { c.Memory = &KindConfig{Kind: "redis"} }, `config.memory.kind "redis" is not one of: sqlite, client`},
		{"resources client", func(c *Config) { c.Resources = &ResourcesConfig{Kind: "client"} }, ""},
		{"resources file", func(c *Config) { c.Resources = &ResourcesConfig{Kind: "file"} }, ""},
		{"resources unknown", func(c *Config) { c.Resources = &ResourcesConfig{Kind: "db"} }, `config.resources.kind "db" is not one of: file, client`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := start(t)
			cfg := memStoreConfig()
			tc.cfg(&cfg)
			if cfg.Memory != nil {
				cfg.Memory.Path = strings.ReplaceAll(cfg.Memory.Path, "{dir}", t.TempDir())
			}
			if tc.want == "" {
				c.initialize(cfg)
				return
			}
			je := c.fail("initialize", InitializeParams{ProtocolVersion: ProtocolVersion, Config: cfg}, -32602, "")
			if !strings.Contains(je.Message, tc.want) {
				t.Fatalf("message = %q, want %q", je.Message, tc.want)
			}
		})
	}
}

// ── (e) concurrency: sub-agents and parallel sessions ────────────────────

func TestClientMemoryParallelSearchesAreServedConcurrently(t *testing.T) {
	const n = 12
	var mu sync.Mutex
	inflight, peak := 0, 0
	release := make(chan struct{})
	var once sync.Once
	mem := clientMemory{conn: connOver(t, func(ctx context.Context, method string, p json.RawMessage) (any, error) {
		mu.Lock()
		inflight++
		if inflight > peak {
			peak = inflight
		}
		if inflight == n {
			once.Do(func() { close(release) })
		}
		mu.Unlock()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		var sp MemorySearchParams
		json.Unmarshal(p, &sp)
		return memory.SearchResult{Total: 1, Returned: 1, Results: []memory.Memory{{Slug: sp.Query}}}, nil
	}), logger: newCaptureLogger()}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := fmt.Sprint("q", i)
			r, err := mem.Search("/p", q, true, 0, 5)
			if err != nil || len(r.Results) != 1 || r.Results[0].Slug != q {
				t.Errorf("search %s = %+v, %v", q, r, err)
			}
		}(i)
	}
	wg.Wait()
	if peak != n {
		t.Fatalf("peak in flight = %d, want %d", peak, n)
	}
}

// ── (f) the contract ─────────────────────────────────────────────────────

func TestClientMemoryPassesTheContract(t *testing.T) {
	storetest.RunContract(t, func(t *testing.T) storetest.Store {
		m := newClientMemImpl()
		return clientMemory{conn: connOver(t, func(ctx context.Context, method string, p json.RawMessage) (any, error) {
			res, err, ok := m.serve(ctx, method, p)
			if !ok {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: method}
			}
			return res, err
		}), logger: newCaptureLogger()}
	})
}
