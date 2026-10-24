package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// seenReq is what the fake upstream actually received.
type seenReq struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
}

// fakeUpstream is a provider's "native API": records every request and answers
// through the supplied handler.
type fakeUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenReq
}

func newFakeUpstream(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body []byte)) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, seenReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), b})
		f.mu.Unlock()
		h(w, r, b)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeUpstream) last(t *testing.T) seenReq {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) == 0 {
		t.Fatal("the upstream received nothing")
	}
	return f.seen[len(f.seen)-1]
}

func (f *fakeUpstream) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.seen) }

var proxySeq atomic.Int64

// proxyEnv starts a server whose agent has a custom OpenAI-compatible provider
// pointing at up, authenticated by a static secret header. Returns the server
// and the provider name.
func proxyEnv(t *testing.T, up *fakeUpstream, opts ServerOptions) (*Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	name := fmt.Sprintf("pxy-%d", proxySeq.Add(1))
	if err := agent.NewOpenAIProvider(name, up.URL,
		agent.ProviderWithHeaders(map[string]string{"X-Api-Key": "SECRET-UPSTREAM-KEY"}),
		agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) { return []types.ModelMeta{{ID: "m"}}, nil }),
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(name) })
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	opts.Logger = logx.NewNilLogger()
	opts.KeepAgentOpen = true
	srv, err := Start(a, "", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, name
}

func proxyURL(srv *Server, name, path string) string {
	return "http://" + srv.Addr() + "/api/providers/" + name + "/proxy" + path
}

func do(t *testing.T, method, url string, body string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// The proxy forwards the caller's body verbatim to <base URL><path>, applies the
// provider's own credentials, drops the caller's, and returns the upstream's
// status, headers and body untouched.
func TestProxyForwardsBodyAuthAndResponse(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("X-Ratelimit-Remaining", "41")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"cmpl-1","echo":true}`))
	})
	srv, name := proxyEnv(t, up, ServerOptions{})

	const reqBody = `{"model":"m","messages":[{"role":"user","content":"hi <b> & ünï"}],"max_tokens":5}`
	resp, body := do(t, "POST", proxyURL(srv, name, "/chat/completions?x=1"), reqBody, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer CALLER-KEY-MUST-NOT-LEAK",
		"X-Api-Key":     "CALLER-XKEY",
		"Cookie":        "session=abc",
		"Origin":        "http://evil.example",
		"X-Custom":      "kept",
	})
	if resp.StatusCode != 200 || string(body) != `{"id":"cmpl-1","echo":true}` {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Ratelimit-Remaining") != "41" {
		t.Error("upstream response headers must pass through")
	}
	got := up.last(t)
	if got.Method != "POST" || got.Path != "/chat/completions" || got.Query != "x=1" {
		t.Errorf("upstream saw %s %s ?%s", got.Method, got.Path, got.Query)
	}
	if string(got.Body) != reqBody {
		t.Errorf("body altered:\n got %s\nwant %s", got.Body, reqBody)
	}
	if got.Header.Get("X-Api-Key") != "SECRET-UPSTREAM-KEY" {
		t.Errorf("provider credential not applied: %q", got.Header.Get("X-Api-Key"))
	}
	for _, h := range []string{"Authorization", "Cookie", "Origin"} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("caller's %s leaked upstream: %q", h, v)
		}
	}
	if got.Header.Get("X-Custom") != "kept" {
		t.Error("ordinary caller headers must be forwarded")
	}
	if strings.Contains(string(body), "SECRET-UPSTREAM-KEY") {
		t.Error("credential in a response body")
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			if strings.Contains(v, "SECRET-UPSTREAM-KEY") {
				t.Errorf("credential in response header %s", k)
			}
		}
	}
}

// An upstream error is the upstream's, not harness's: status and body as-is.
func TestProxyPassesUpstreamErrorsThrough(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"type":"rate_limit"}}`))
	})
	srv, name := proxyEnv(t, up, ServerOptions{})
	resp, body := do(t, "POST", proxyURL(srv, name, "/chat/completions"), `{}`, nil)
	if resp.StatusCode != 429 || string(body) != `{"error":{"type":"rate_limit"}}` {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

// SSE arrives event by event as the upstream emits it — not buffered to the end.
func TestProxyStreamsSSEIncrementally(t *testing.T) {
	release := make(chan struct{})
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"n\":1}\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: {\"n\":2}\n\ndata: [DONE]\n\n")
	})
	srv, name := proxyEnv(t, up, ServerOptions{})

	resp, err := http.Post(proxyURL(srv, name, "/chat/completions"), "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() { l, _ := br.ReadString('\n'); first <- l }()
	select {
	case l := <-first:
		if l != "data: {\"n\":1}\n" {
			t.Fatalf("first line = %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first event not delivered while the upstream is still streaming — the proxy is buffering")
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), `{"n":2}`) || !strings.Contains(string(rest), "[DONE]") {
		t.Errorf("rest = %q", rest)
	}
}

// Closing the connection cancels the upstream call.
func TestProxyCancelsUpstreamWhenCallerLeaves(t *testing.T) {
	upstreamDone := make(chan struct{})
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"n\":1}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // returns only when the proxy drops its upstream connection
		close(upstreamDone)
	})
	srv, name := proxyEnv(t, up, ServerOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", proxyURL(srv, name, "/chat/completions"), strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bufio.NewReader(resp.Body).ReadString('\n')
	cancel()
	resp.Body.Close()
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream call was not cancelled when the caller disconnected")
	}
}

// A silent upstream stream is aborted by the idle watchdog, and the caller sees
// a broken stream rather than one that merely ends.
func TestProxyIdleStreamIsAborted(t *testing.T) {
	old := proxyIdleTimeout
	proxyIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { proxyIdleTimeout = old })
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"n\":1}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	srv, name := proxyEnv(t, up, ServerOptions{})
	resp, err := http.Post(proxyURL(srv, name, "/chat/completions"), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(resp.Body); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("an idle-aborted stream must surface as a read error, not a clean EOF")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle upstream never aborted")
	}
}

func TestProxyHarnessErrors(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) { w.Write([]byte(`{}`)) })
	srv, name := proxyEnv(t, up, ServerOptions{ProxyMaxBodyBytes: 1024})
	n0 := up.count()

	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"unknown provider", "POST", "/api/providers/nope/proxy/chat/completions", `{}`, 404},
		{"path not allowed", "POST", "/api/providers/" + name + "/proxy/admin/keys", `{}`, 403},
		{"method not allowed", "DELETE", "/api/providers/" + name + "/proxy/chat/completions", ``, 403},
		{"dot segments", "POST", "/api/providers/" + name + "/proxy/chat/../admin", `{}`, 400},
		{"encoded slash", "POST", "/api/providers/" + name + "/proxy/chat%2F..%2Fadmin", `{}`, 400},
		{"oversize body", "POST", "/api/providers/" + name + "/proxy/chat/completions", strings.Repeat("x", 2048), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, "http://"+srv.Addr()+tc.path, strings.NewReader(tc.body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, b, tc.want)
			}
			if !bytes.Contains(b, []byte(`"message"`)) {
				t.Errorf("not harness's error shape: %s", b)
			}
		})
	}
	if up.count() != n0 {
		t.Error("a rejected request still reached the upstream")
	}
}

// A provider that isn't connected is a 409, and nothing is sent.
func TestProxyInactiveProviderIs409(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	// minimax has no credentials in this isolated HOME.
	resp, b := do(t, "POST", "http://"+srv.Addr()+"/api/providers/minimax/proxy/chat/completions", `{}`, nil)
	if resp.StatusCode != 409 || !strings.Contains(string(b), "connect") {
		t.Fatalf("status=%d body=%s; want 409 with an actionable message", resp.StatusCode, b)
	}
}

// Nothing session-shaped is created.
func TestProxyCreatesNoSession(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) { w.Write([]byte(`{}`)) })
	srv, name := proxyEnv(t, up, ServerOptions{})
	list := func() string {
		_, b := do(t, "GET", "http://"+srv.Addr()+"/api/sessions", "", nil)
		return string(b)
	}
	before := list()
	do(t, "POST", proxyURL(srv, name, "/chat/completions"), `{}`, nil)
	if after := list(); after != before {
		t.Errorf("session list changed: %s → %s", before, after)
	}
}

// A provider registered at runtime is reachable without any other change, and
// is gone once unregistered.
func TestProxyRuntimeRegisteredProvider(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) { w.Write([]byte(`{"ok":1}`)) })
	srv, _ := proxyEnv(t, up, ServerOptions{})
	late := fmt.Sprintf("late-%d", proxySeq.Add(1))
	if resp, _ := do(t, "POST", proxyURL(srv, late, "/chat/completions"), `{}`, nil); resp.StatusCode != 404 {
		t.Fatalf("before registering: %d", resp.StatusCode)
	}
	if err := agent.NewOpenAIProvider(late, up.URL, agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) { return nil, nil })); err != nil {
		t.Fatal(err)
	}
	resp, b := do(t, "POST", proxyURL(srv, late, "/chat/completions"), `{}`, nil)
	if resp.StatusCode != 200 || string(b) != `{"ok":1}` {
		t.Fatalf("after registering: %d %s", resp.StatusCode, b)
	}
	agent.UnregisterCustomProvider(late)
	if resp, _ := do(t, "POST", proxyURL(srv, late, "/chat/completions"), `{}`, nil); resp.StatusCode != 404 {
		t.Errorf("after unregistering: %d", resp.StatusCode)
	}
}

// Concurrent calls are served in parallel (one slow call doesn't hold another).
func TestProxyConcurrentCallsDontBlockEachOther(t *testing.T) {
	gate := make(chan struct{})
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if string(body) == "slow" {
			<-gate
		}
		w.Write([]byte(`{}`))
	})
	srv, name := proxyEnv(t, up, ServerOptions{})
	slowDone := make(chan struct{})
	go func() { do(t, "POST", proxyURL(srv, name, "/chat/completions"), "slow", nil); close(slowDone) }()
	time.Sleep(100 * time.Millisecond)
	fast := make(chan struct{})
	go func() { do(t, "POST", proxyURL(srv, name, "/chat/completions"), "fast", nil); close(fast) }()
	select {
	case <-fast:
	case <-time.After(3 * time.Second):
		t.Fatal("a fast call waited on a slow one")
	}
	close(gate)
	<-slowDone
}

func TestProxyGetModels(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	})
	srv, name := proxyEnv(t, up, ServerOptions{})
	resp, b := do(t, "GET", proxyURL(srv, name, "/models"), "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"id":"m"`) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if got := up.last(t); got.Method != "GET" || got.Path != "/models" {
		t.Errorf("upstream saw %s %s", got.Method, got.Path)
	}
}

var _ = json.Marshal

// ── native providers, end to end through the server ──────────────────────

// nativeServer swaps in fresh credential stores holding the given credentials,
// builds the real providers (so their Upstream() resolves those credentials),
// points them at up, and serves them.
func nativeServer(t *testing.T, up *fakeUpstream, creds map[string]config.ProviderCredential) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	for name, c := range creds {
		if err := config.GetCredentialsManager().SetCredential(name, c); err != nil {
			t.Fatal(err)
		}
	}
	oauth, _ := providers.NewClaudeOAuth()
	codex, _ := providers.NewCodexOAuth()
	list := []providers.Provider{
		providers.RedirectForTest(oauth, up.URL),
		providers.RedirectForTest(codex, up.URL),
		providers.RedirectForTest(providers.NewAnthropic(), up.URL),
	}
	t.Cleanup(providers.SetForTest(list))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func freshOAuth(token string) config.ProviderCredential {
	return config.ProviderCredential{Type: "oauth", AccessToken: token, RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
}

// claude-oauth, streaming: the caller sends a plain Anthropic request with its
// own tool names; the backend sees Claude Code's request (identity block,
// WebFetch, token, billing header) and the stream that comes back carries the
// caller's tool names again.
func TestProxyClaudeOAuthEndToEnd(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_123")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"WebFetch\",\"input\":{}}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"url\\\":\\\"WebFetch\\\"}\"}}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"mcp__x__KaibanAPI\",\"input\":{}}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := nativeServer(t, up, map[string]config.ProviderCredential{"claude-oauth": freshOAuth("CLAUDE-OAUTH-TOKEN")})

	body := `{"model":"claude-sonnet-5","max_tokens":50,"stream":true,"system":"Be terse.",
	  "messages":[{"role":"user","content":"get it"}],
	  "tools":[{"name":"Fetch","description":"d","input_schema":{"type":"object"}},{"name":"KaibanAPI","description":"d","input_schema":{"type":"object"}}]}`
	resp, got := do(t, "POST", "http://"+srv.Addr()+"/api/providers/claude-oauth/proxy/v1/messages", body,
		map[string]string{"Authorization": "Bearer caller", "x-api-key": "caller", "anthropic-version": "2023-06-01", "anthropic-beta": "my-beta-1"})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, got)
	}

	seen := up.last(t)
	if seen.Path != "/v1/messages" {
		t.Errorf("upstream path = %s", seen.Path)
	}
	if seen.Header.Get("Authorization") != "Bearer CLAUDE-OAUTH-TOKEN" || seen.Header.Get("X-Api-Key") != "" {
		t.Errorf("auth = %q / x-api-key %q", seen.Header.Get("Authorization"), seen.Header.Get("X-Api-Key"))
	}
	if beta := seen.Header.Get("Anthropic-Beta"); !strings.Contains(beta, "oauth-2025-04-20") || !strings.Contains(beta, "my-beta-1") {
		t.Errorf("anthropic-beta = %q: want the provider's betas AND the caller's merged", beta)
	}
	if seen.Header.Get("X-Anthropic-Billing-Header") == "" {
		t.Error("billing header missing")
	}
	var sent struct {
		System []struct{ Text string }
		Tools  []struct{ Name string }
	}
	json.Unmarshal(seen.Body, &sent)
	if len(sent.System) != 2 || !strings.HasPrefix(sent.System[0].Text, "You are Claude Code") || sent.System[1].Text != "Be terse." {
		t.Errorf("system sent upstream = %+v", sent.System)
	}
	if len(sent.Tools) != 2 || sent.Tools[0].Name != "WebFetch" || sent.Tools[1].Name != "mcp__x__KaibanAPI" {
		t.Errorf("tools sent upstream = %+v", sent.Tools)
	}

	// The stream the caller gets speaks ITS tool names, framing intact.
	s := string(got)
	if !strings.Contains(s, `"name":"Fetch"`) || !strings.Contains(s, `"name":"KaibanAPI"`) ||
		strings.Contains(s, `"name":"WebFetch"`) || strings.Contains(s, "mcp__x__") {
		t.Errorf("tool names not mapped back in the stream:\n%s", s)
	}
	if !strings.Contains(s, `\"url\":\"WebFetch\"`) {
		t.Errorf("tool ARGUMENT text must be left alone:\n%s", s)
	}
	if !strings.Contains(s, "event: message_start") || !strings.Contains(s, "event: message_stop") {
		t.Errorf("SSE framing lost:\n%s", s)
	}
	if resp.Header.Get("Request-Id") != "req_123" {
		t.Error("upstream response headers must pass through")
	}
	if strings.Contains(s, "CLAUDE-OAUTH-TOKEN") {
		t.Error("token in the response")
	}
}

// claude-oauth, non-streaming: a JSON message comes back with the caller's names.
func TestProxyClaudeOAuthNonStreaming(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","content":[{"type":"tool_use","id":"t","name":"WebFetch","input":{}}],"usage":{"input_tokens":3}}`)
	})
	srv := nativeServer(t, up, map[string]config.ProviderCredential{"claude-oauth": freshOAuth("tok")})
	resp, got := do(t, "POST", "http://"+srv.Addr()+"/api/providers/claude-oauth/proxy/v1/messages",
		`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"x"}]}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(got), `"name":"Fetch"`) || !strings.Contains(string(got), `"input_tokens":3`) {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
}

// A malformed body for claude-oauth is the caller's 400, before anything is sent.
func TestProxyClaudeOAuthBadBodyIs400(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {})
	srv := nativeServer(t, up, map[string]config.ProviderCredential{"claude-oauth": freshOAuth("tok")})
	resp, b := do(t, "POST", "http://"+srv.Addr()+"/api/providers/claude-oauth/proxy/v1/messages", `not json`, nil)
	if resp.StatusCode != 400 || up.count() != 0 {
		t.Fatalf("status=%d upstream calls=%d body=%s", resp.StatusCode, up.count(), b)
	}
}

// codex-oauth: identity headers only; the body is the caller's, byte for byte.
func TestProxyCodexOAuth(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
	})
	srv := nativeServer(t, up, map[string]config.ProviderCredential{
		"codex-oauth": {Type: "oauth", AccessToken: "CODEX-TOKEN", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acct-9"},
	})
	const body = `{"model":"gpt","instructions":"x","input":[],"stream":true,"store":false}`
	resp, got := do(t, "POST", "http://"+srv.Addr()+"/api/providers/codex-oauth/proxy/responses", body,
		map[string]string{"ChatGPT-Account-ID": "caller-acct", "Authorization": "Bearer caller"})
	if resp.StatusCode != 200 || !strings.Contains(string(got), "response.completed") {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
	seen := up.last(t)
	if seen.Path != "/backend-api/codex/responses" {
		t.Errorf("path = %s", seen.Path)
	}
	if string(seen.Body) != body {
		t.Errorf("body must be untouched:\n got %s", seen.Body)
	}
	for h, want := range map[string]string{
		"Authorization": "Bearer CODEX-TOKEN", "Originator": "", "Chatgpt-Account-Id": "acct-9",
	} {
		got := seen.Header.Get(h)
		if want == "" && got == "" {
			t.Errorf("header %s missing", h)
		}
		if want != "" && got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
	if seen.Header.Get("Session-Id") == "" || !strings.HasPrefix(seen.Header.Get("User-Agent"), "codex_cli_rs/") {
		t.Errorf("identity headers: session-id=%q ua=%q", seen.Header.Get("Session-Id"), seen.Header.Get("User-Agent"))
	}
}

// anthropic (API key): x-api-key from the store, anthropic-version only as a
// default (a pinned one is kept), body verbatim, count_tokens allowed.
func TestProxyAnthropicAPIKey(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":7}`)
	})
	srv := nativeServer(t, up, map[string]config.ProviderCredential{"anthropic": {Type: "api_key", APIKey: "sk-ant-STORED"}})
	const body = `{"model":"m","messages":[{"role":"user","content":"a < b"}]}`

	resp, got := do(t, "POST", "http://"+srv.Addr()+"/api/providers/anthropic/proxy/v1/messages/count_tokens", body, nil)
	if resp.StatusCode != 200 || string(got) != `{"input_tokens":7}` {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
	seen := up.last(t)
	if seen.Header.Get("X-Api-Key") != "sk-ant-STORED" || seen.Header.Get("Anthropic-Version") != "2023-06-01" || string(seen.Body) != body {
		t.Errorf("x-api-key=%q version=%q body=%s", seen.Header.Get("X-Api-Key"), seen.Header.Get("Anthropic-Version"), seen.Body)
	}
	do(t, "POST", "http://"+srv.Addr()+"/api/providers/anthropic/proxy/v1/messages", body, map[string]string{"anthropic-version": "2099-01-01", "x-api-key": "caller"})
	if v := up.last(t).Header.Get("Anthropic-Version"); v != "2099-01-01" {
		t.Errorf("a version pinned by the caller must be kept, got %q", v)
	}
	if k := up.last(t).Header.Get("X-Api-Key"); k != "sk-ant-STORED" {
		t.Errorf("x-api-key = %q: the caller's key must be replaced by the stored one", k)
	}
}

// An OAuth provider with no credentials is a 409, not a request to Anthropic.
func TestProxyOAuthWithoutCredentialsIs409(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {})
	srv := nativeServer(t, up, nil)
	for _, name := range []string{"claude-oauth", "codex-oauth", "anthropic"} {
		resp, b := do(t, "POST", "http://"+srv.Addr()+"/api/providers/"+name+"/proxy/v1/messages", `{}`, nil)
		if resp.StatusCode != 409 {
			t.Errorf("%s: status %d (%s), want 409", name, resp.StatusCode, b)
		}
	}
	if up.count() != 0 {
		t.Error("an unconnected provider still reached the upstream")
	}
}

// The hand-written OpenAPI spec documents the proxy and stays valid.
func TestOpenAPISpecDocumentsProviderProxy(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(openAPISpecJSON("127.0.0.1:0")), &spec); err != nil {
		t.Fatalf("spec is not valid JSON: %v", err)
	}
	ops := spec.Paths["/api/providers/{name}/proxy/{path}"]
	for _, m := range []string{"post", "get"} {
		if _, ok := ops[m]; !ok {
			t.Errorf("spec lacks %s /api/providers/{name}/proxy/{path}", m)
		}
	}
}
