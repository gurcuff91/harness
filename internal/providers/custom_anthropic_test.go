package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// anthropicDouble is an Anthropic Messages-compatible endpoint: it records every
// request and serves /v1/models (paginated, with capabilities) and /v1/messages
// (an SSE reply).
type anthropicDouble struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
	// models are the ids served, in order, page by page.
	models   []string
	pageSize int
}

type recorded struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
}

func newAnthropicDouble(t *testing.T, models ...string) *anthropicDouble {
	t.Helper()
	d := &anthropicDouble{models: models, pageSize: 2}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.reqs = append(d.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), b})
		d.mu.Unlock()
		switch r.URL.Path {
		case "/v1/models":
			after := r.URL.Query().Get("after_id")
			start := 0
			if after != "" {
				for i, id := range d.models {
					if id == after {
						start = i + 1
					}
				}
			}
			end := min(start+d.pageSize, len(d.models))
			var data []string
			for _, id := range d.models[start:end] {
				data = append(data, fmt.Sprintf(`{"id":%q,"display_name":"Name of %s","max_input_tokens":500000,"max_tokens":32000,
				  "capabilities":{"image_input":{"supported":true},"thinking":{"supported":true,"types":{"adaptive":{"supported":true},"enabled":{"supported":false}}},
				  "effort":{"low":{"supported":true},"high":{"supported":true},"max":{"supported":false}}}}`, id, id))
			}
			last := ""
			if end > start {
				last = d.models[end-1]
			}
			fmt.Fprintf(w, `{"data":[%s],"has_more":%v,"last_id":%q}`, strings.Join(data, ","), end < len(d.models), last)
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n")
			io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"pong\"}}\n\n")
			io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
			io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *anthropicDouble) calls(path string) []recorded {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []recorded
	for _, r := range d.reqs {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func newCA(d *anthropicDouble, cfg types.CustomProvider) *CustomAnthropic {
	cfg.Type, cfg.URL = "anthropic", d.URL
	return NewCustomAnthropic("shared-claude", cfg)
}

func TestCustomAnthropic_Identity(t *testing.T) {
	d := newAnthropicDouble(t)
	o := newCA(d, types.CustomProvider{})
	if o.Name() != "shared-claude" || o.DisplayName() != "shared-claude" || o.Dialect() != DialectAnthropic {
		t.Errorf("name=%q display=%q dialect=%q", o.Name(), o.DisplayName(), o.Dialect())
	}
	if n := newCA(d, types.CustomProvider{Display: "Shared"}); n.DisplayName() != "Shared" {
		t.Errorf("display = %q", n.DisplayName())
	}
	if !o.IsActive() || o.CredentialType() != types.CredTypeNone || o.ActivationSource() != ActivationAuto {
		t.Error("a custom provider is always active, with no credential")
	}
	if err := o.Connect(types.Credentials{}); err == nil {
		t.Error("Connect must be rejected")
	}
	if err := o.Disconnect(); err == nil {
		t.Error("Disconnect must be rejected")
	}
}

// FetchModels follows the API's pagination, keeps what the API reports
// (capabilities, limits, names — all marked provider), and accepts any id.
func TestCustomAnthropic_FetchModelsPaginatesAndKeepsCapabilities(t *testing.T) {
	d := newAnthropicDouble(t, "claude-a", "claude-b", "claude-c", "weird-model-d", "claude-e")
	o := newCA(d, types.CustomProvider{Headers: map[string]string{"x-api-key": "k1"}})
	metas, err := o.FetchModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 5 {
		t.Fatalf("got %d models, want all 5 across 3 pages (and a non-'c' id must NOT be filtered): %v", len(metas), ids(metas))
	}
	if n := len(d.calls("/v1/models")); n != 3 {
		t.Errorf("%d requests to /v1/models, want 3 pages", n)
	}
	first := d.calls("/v1/models")[0]
	if !strings.Contains(first.Query, "limit=1000") {
		t.Errorf("first page query = %q, want the max page size", first.Query)
	}
	if !strings.Contains(d.calls("/v1/models")[1].Query, "after_id=claude-b") {
		t.Errorf("second page must continue after the first's last_id: %q", d.calls("/v1/models")[1].Query)
	}
	m := o.ModelMeta("claude-a")
	if m == nil || m.ContextWindow != 500000 || m.MaxTokens != 32000 || m.DisplayName != "Name of claude-a" || !m.Vision || !m.Thinking || !m.ThinkingAdaptive || m.ThinkingLegacy {
		t.Fatalf("capabilities lost: %+v", m)
	}
	if !m.EffortLevels["low"] || !m.EffortLevels["high"] || m.EffortLevels["max"] {
		t.Errorf("effort levels = %v", m.EffortLevels)
	}
	for f, want := range map[string]string{types.FieldContextWindow: types.SourceProvider, types.FieldMaxTokens: types.SourceProvider, types.FieldVision: types.SourceProvider} {
		if m.Sources[f] != want {
			t.Errorf("sources[%s] = %q, want %q", f, m.Sources[f], want)
		}
	}
	// auth: exactly the configured headers + the version default; no invented credential
	if first.Header.Get("x-api-key") != "k1" || first.Header.Get("anthropic-version") != "2023-06-01" || first.Header.Get("Authorization") != "" {
		t.Errorf("headers = %v", first.Header)
	}
}

func ids(ms []types.ModelMeta) []string {
	var o []string
	for _, m := range ms {
		o = append(o, m.ID)
	}
	return o
}

// A pagination loop can't be trapped by a server that never stops saying has_more.
func TestCustomAnthropic_FetchModelsPaginationIsBounded(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprintf(w, `{"data":[{"id":"c%d"}],"has_more":true,"last_id":"c%d"}`, n, n)
	}))
	defer srv.Close()
	o := NewCustomAnthropic("p", types.CustomProvider{Type: "anthropic", URL: srv.URL})
	if _, err := o.FetchModels(); err != nil {
		t.Fatal(err)
	}
	if n > anthropicModelsMaxPages {
		t.Errorf("%d requests, the loop must stop at %d", n, anthropicModelsMaxPages)
	}
	// and one whose last_id never advances
	n = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		io.WriteString(w, `{"data":[{"id":"c1"}],"has_more":true,"last_id":"c1"}`)
	}))
	defer srv2.Close()
	if _, err := NewCustomAnthropic("p", types.CustomProvider{Type: "anthropic", URL: srv2.URL}).FetchModels(); err != nil || n > 2 {
		t.Errorf("a stuck last_id looped %d times (err=%v)", n, err)
	}
}

func TestCustomAnthropic_FetchModelsUsesModelsURLWhenSet(t *testing.T) {
	d := newAnthropicDouble(t, "claude-a")
	o := NewCustomAnthropic("p", types.CustomProvider{Type: "anthropic", URL: "http://127.0.0.1:1", ModelsURL: d.URL + "/v1/models"})
	if _, err := o.FetchModels(); err != nil {
		t.Fatal(err)
	}
	if len(d.calls("/v1/models")) != 1 {
		t.Errorf("the models_url override was not used")
	}
}

func TestCustomAnthropic_FetchModelsErrors(t *testing.T) {
	for status, want := range map[int]string{401: "invalid API key", 403: "invalid API key"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		_, err := NewCustomAnthropic("p", types.CustomProvider{Type: "anthropic", URL: srv.URL}).FetchModels()
		srv.Close()
		if err == nil || err.Error() != want {
			t.Errorf("HTTP %d: %v, want %q", status, err, want)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	if _, err := NewCustomAnthropic("p", types.CustomProvider{Type: "anthropic", URL: srv.URL}).FetchModels(); err == nil {
		t.Error("a 500 must be an error")
	}
	if _, err := NewCustomAnthropic("p", types.CustomProvider{Type: "anthropic", URL: "http://127.0.0.1:1"}).FetchModels(); err == nil {
		t.Error("an unreachable endpoint must be an error")
	}
}

// The wire a session sends: a PLAIN Anthropic request — no Claude Code identity
// block, no tool renaming — to <url>/v1/messages with the configured headers.
func TestCustomAnthropic_CompleteStreamSendsAPlainRequest(t *testing.T) {
	d := newAnthropicDouble(t, "claude-a")
	o := newCA(d, types.CustomProvider{Headers: map[string]string{"x-api-key": "k1", "X-Extra": "e"}})
	if _, err := o.FetchModels(); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	resp, err := o.CompleteStream(context.Background(), &types.Request{
		Model: "claude-a", SystemPrompt: "Be terse.", MaxTokens: 100,
		Messages: []types.Message{types.NewUserTextMessage("Say pong")},
		Tools:    []types.ToolDef{{Name: "Fetch", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, func(e types.StreamEvent) {
		if e.Type == types.StreamTextDelta {
			text.WriteString(e.Delta)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "pong" || text.String() != "pong" || resp.Usage.InputTokens != 3 || resp.Usage.OutputTokens != 1 {
		t.Errorf("response = %q / streamed %q / %+v", resp.Text, text.String(), resp.Usage)
	}
	got := d.calls("/v1/messages")
	if len(got) != 1 {
		t.Fatalf("%d message requests", len(got))
	}
	r := got[0]
	if r.Method != "POST" || r.Header.Get("x-api-key") != "k1" || r.Header.Get("X-Extra") != "e" || r.Header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("headers = %v", r.Header)
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("x-app") != "" || r.Header.Get("x-anthropic-billing-header") != "" {
		t.Errorf("an identity/credential header was invented: %v", r.Header)
	}
	var body struct {
		System   json.RawMessage `json:"system"`
		Tools    []struct{ Name string }
		Stream   bool
		Thinking map[string]any
	}
	json.Unmarshal(r.Body, &body)
	if !body.Stream || strings.Contains(string(body.System), "Claude Code") || !strings.Contains(string(body.System), "Be terse.") {
		t.Errorf("system = %s (must be the caller's, with NO Claude Code identity)", body.System)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "Fetch" {
		t.Errorf("tools = %+v: names must NOT be remapped (the proxy on the other side does that)", body.Tools)
	}
}

func TestCustomAnthropic_ThinkingBetaAndConfiguredBeta(t *testing.T) {
	d := newAnthropicDouble(t)
	send := func(o *CustomAnthropic, level string) http.Header {
		o.CompleteStream(context.Background(), &types.Request{Model: "m", MaxTokens: 5000, ThinkingLevel: level, Messages: []types.Message{types.NewUserTextMessage("x")}}, nil)
		c := d.calls("/v1/messages")
		return c[len(c)-1].Header
	}
	// unknown model → legacy thinking shape → interleaved beta
	o := newCA(d, types.CustomProvider{})
	if b := send(o, "medium"); !strings.Contains(b.Get("anthropic-beta"), "interleaved-thinking") {
		t.Errorf("legacy thinking should add the interleaved beta, got %q", b.Get("anthropic-beta"))
	}
	if b := send(o, "off"); b.Get("anthropic-beta") != "" {
		t.Errorf("no thinking, no beta: %q", b.Get("anthropic-beta"))
	}
	// a configured anthropic-beta is kept AND merged with the one we add
	o2 := newCA(d, types.CustomProvider{Headers: map[string]string{"Anthropic-Beta": "my-beta"}})
	if b := send(o2, "off"); b.Get("anthropic-beta") != "my-beta" {
		t.Errorf("configured beta lost: %q", b.Get("anthropic-beta"))
	}
	b := send(o2, "medium").Get("anthropic-beta")
	if !strings.Contains(b, "my-beta") || !strings.Contains(b, "interleaved-thinking") {
		t.Errorf("configured and added betas must merge: %q", b)
	}
	// a configured anthropic-version wins over the default
	o3 := newCA(d, types.CustomProvider{Headers: map[string]string{"anthropic-version": "2099-01-01"}})
	if v := send(o3, "off").Get("anthropic-version"); v != "2099-01-01" {
		t.Errorf("anthropic-version = %q, the configured one must win", v)
	}
}

// The proxy side: a custom anthropic provider can itself be proxied (chaining).
func TestCustomAnthropic_Upstream(t *testing.T) {
	d := newAnthropicDouble(t)
	o := newCA(d, types.CustomProvider{URL: d.URL + "/", Headers: map[string]string{"x-api-key": "k"}})
	o.baseURL = d.URL + "/" // trailing slash must not double up
	up, err := o.Upstream()
	if err != nil {
		t.Fatal(err)
	}
	if up.BaseURL != d.URL || up.Headers["x-api-key"] != "k" || up.Defaults["anthropic-version"] != "2023-06-01" || up.Rewrite != nil || up.RewriteResponse != nil {
		t.Errorf("upstream = %+v: a plain passthrough, no rewrite (the far proxy does it)", up)
	}
	for _, c := range []struct {
		m, p string
		ok   bool
	}{{"POST", "/v1/messages", true}, {"POST", "/v1/messages/count_tokens", true}, {"GET", "/v1/models", true},
		{"GET", "/v1/models/claude-x", true}, {"GET", "/v1/models/a/b", false}, {"DELETE", "/v1/models/claude-x", false}, // models.retrieve()
		{"GET", "/v1/messages", false}, {"POST", "/v1/complete", false}} {
		if up.Allowed(c.m, c.p) != c.ok {
			t.Errorf("Allowed(%s %s) = %v", c.m, c.p, !c.ok)
		}
	}
	// the returned header map is a copy
	up.Headers["x-api-key"] = "changed"
	if o.headers["x-api-key"] != "k" {
		t.Error("Upstream() leaked the provider's own header map")
	}
}

// ── registry integration ─────────────────────────────────────────────────

func TestBuildCustomProviders_AnthropicType(t *testing.T) {
	got := buildCustomProviders(map[string]types.CustomProvider{
		"a-oa":  {Type: "openai", URL: "http://x/v1"},
		"b-an":  {Type: "anthropic", URL: "http://x"},
		"c-off": {Type: "anthropic", URL: "http://x", Disabled: true},
	})
	if len(got) != 2 {
		t.Fatalf("got %d providers, want 2 (disabled skipped)", len(got))
	}
	if _, ok := got[0].(*CustomOpenAI); !ok {
		t.Errorf("a-oa is %T", got[0])
	}
	if _, ok := got[1].(*CustomAnthropic); !ok || got[1].Dialect() != DialectAnthropic {
		t.Errorf("b-an is %T dialect %q", got[1], got[1].Dialect())
	}
}

func TestRegisterAnthropic(t *testing.T) {
	withCleanRegistry(t)
	hook := func() ([]types.ModelMeta, error) { return []types.ModelMeta{{ID: "m1"}}, nil }
	if err := RegisterAnthropic("sdk-claude", "http://x", "SDK Claude", map[string]string{"x-api-key": "k"}, hook); err != nil {
		t.Fatal(err)
	}
	p := Find("sdk-claude")
	ca, ok := p.(*CustomAnthropic)
	if !ok || ca.DisplayName() != "SDK Claude" || ca.Dialect() != DialectAnthropic || !ca.programmatic {
		t.Fatalf("registered = %T %+v", p, p)
	}
	if ms, err := ca.FetchModels(); err != nil || len(ms) != 1 || ca.ModelMeta("m1") == nil {
		t.Errorf("the fetch hook was not used: %v %v", ms, err)
	}
	if err := RegisterAnthropic("sdk-claude", "http://y", "", nil, nil); err == nil {
		t.Error("duplicate name must be rejected")
	}
	if err := RegisterAnthropic("anthropic", "http://y", "", nil, nil); err == nil {
		t.Error("a reserved built-in name must be rejected")
	}
	if err := Unregister("sdk-claude"); err != nil || Find("sdk-claude") != nil {
		t.Errorf("a programmatic anthropic provider must be removable: %v", err)
	}
	if err := Unregister("anthropic"); err == nil {
		t.Error("the built-in must stay unremovable")
	}
}

// Resolve works end to end for a custom anthropic provider.
func TestCustomAnthropic_ResolveEndToEnd(t *testing.T) {
	withCleanRegistry(t)
	d := newAnthropicDouble(t, "claude-a")
	if err := RegisterAnthropic("e2e-claude", d.URL, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	p, model, err := Resolve("e2e-claude/claude-a")
	if err != nil || model != "claude-a" || p.Name() != "e2e-claude" {
		t.Fatalf("Resolve: %v %q", err, model)
	}
	if _, _, err := Resolve("e2e-claude/nope"); err == nil {
		t.Error("an unknown model must not resolve")
	}
}

// ── the NATIVE providers share fetchAnthropicModelsFrom: pin their behavior ──

// fetchAnthropicModels (anthropic + claude-oauth) tries x-api-key first and, if
// that is refused, retries ONCE as an OAuth bearer token with the Claude Code
// beta — the behavior the refactor into a shared fetcher must keep.
func TestFetchAnthropicModelsKeepsTheOAuthRetry(t *testing.T) {
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		if r.Header.Get("x-api-key") != "" { // the key path is refused...
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"data":[{"id":"claude-x","max_input_tokens":1000,"max_tokens":10}],"has_more":false}`)
	}))
	defer srv.Close()
	metas, err := fetchAnthropicModelsFrom(srv.URL, "anthropic", isClaudeModelID, func(req *http.Request, attempt int) {
		req.Header.Set("anthropic-version", "2023-06-01")
		if attempt == 0 {
			req.Header.Set("x-api-key", "tok")
			return
		}
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		req.Header.Set("x-app", "cli")
	}, 2)
	if err != nil || len(metas) != 1 || metas[0].ID != "claude-x" {
		t.Fatalf("metas=%v err=%v", ids(metas), err)
	}
	if len(seen) != 2 || seen[0].Get("x-api-key") != "tok" || seen[1].Get("Authorization") != "Bearer tok" ||
		seen[1].Get("x-api-key") != "" || !strings.Contains(seen[1].Get("anthropic-beta"), "oauth-2025-04-20") {
		t.Errorf("attempts = %v", seen)
	}
	// both refused → "invalid API key", not a retry loop
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer deny.Close()
	if _, err := fetchAnthropicModelsFrom(deny.URL, "anthropic", isClaudeModelID, func(req *http.Request, _ int) {}, 2); err == nil || err.Error() != "invalid API key" {
		t.Errorf("both refused: %v", err)
	}
}

// Anthropic's OWN catalog keeps its historical "claude…" filter; a custom
// endpoint's models are all kept.
func TestAnthropicModelFilterOnlyForTheNativeCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"claude-x"},{"id":"embedding-y"},{"id":""}],"has_more":false}`)
	}))
	defer srv.Close()
	noAuth := func(req *http.Request, _ int) {}
	native, _ := fetchAnthropicModelsFrom(srv.URL, "anthropic", isClaudeModelID, noAuth, 1)
	if got := ids(native); len(got) != 1 || got[0] != "claude-x" {
		t.Errorf("native filter kept %v, want [claude-x]", got)
	}
	custom, _ := fetchAnthropicModelsFrom(srv.URL, "x", nil, noAuth, 1)
	if got := ids(custom); len(got) != 2 {
		t.Errorf("custom kept %v, want both non-empty ids", got)
	}
}

// The Anthropic API paginates (default page 20): the native provider's list is
// now complete beyond one page too.
func TestFetchAnthropicModelsFollowsPagination(t *testing.T) {
	d := newAnthropicDouble(t, "claude-1", "claude-2", "claude-3", "claude-4", "claude-5")
	metas, err := fetchAnthropicModelsFrom(d.URL, "anthropic", isClaudeModelID, func(req *http.Request, _ int) {}, 1)
	if err != nil || len(metas) != 5 {
		t.Fatalf("got %v err=%v, want all 5 over 3 pages", ids(metas), err)
	}
}

// An adaptive-only model (the real claude-*-5 family) REJECTS {"thinking":
// {"type":"disabled"}} and wants {"type":"adaptive"} + output_config.effort. The
// custom provider must shape thinking from the model's own reported
// capabilities (as the built-in does), not guess: that is what makes a model
// served by another harness usable at all.
func TestCustomAnthropic_ThinkingFollowsTheModelsCapabilities(t *testing.T) {
	d := newAnthropicDouble(t, "claude-adaptive") // the double reports adaptive:true, enabled:false
	o := newCA(d, types.CustomProvider{})
	if _, err := o.FetchModels(); err != nil {
		t.Fatal(err)
	}
	send := func(model, level string) map[string]any {
		o.CompleteStream(context.Background(), &types.Request{Model: model, MaxTokens: 1000, ThinkingLevel: level, Messages: []types.Message{types.NewUserTextMessage("x")}}, nil)
		c := d.calls("/v1/messages")
		var body map[string]any
		json.Unmarshal(c[len(c)-1].Body, &body)
		return body
	}
	for _, level := range []string{"off", "", "high"} {
		body := send("claude-adaptive", level)
		th, _ := body["thinking"].(map[string]any)
		if th["type"] != "adaptive" {
			t.Errorf("level %q on an adaptive-only model: thinking = %v, want type adaptive (disabled/enabled are rejected by the API)", level, th)
		}
		if _, ok := body["output_config"]; !ok {
			t.Errorf("level %q: adaptive thinking needs output_config.effort", level)
		}
	}
	// a model the provider never listed has no capabilities: the legacy shape
	if th := send("claude-unlisted", "off")["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Errorf("unknown model, off: thinking = %v, want the legacy disabled shape", th)
	}
}

// A base URL typed with a trailing slash must not produce "//v1/messages".
func TestCustomAnthropic_TrailingSlashInURL(t *testing.T) {
	d := newAnthropicDouble(t, "claude-a")
	o := NewCustomAnthropic("p", types.CustomProvider{Type: "anthropic", URL: d.URL + "/"})
	if _, err := o.FetchModels(); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CompleteStream(context.Background(), &types.Request{Model: "claude-a", MaxTokens: 10, Messages: []types.Message{types.NewUserTextMessage("x")}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range d.reqs {
		if strings.Contains(r.Path, "//") || (r.Path != "/v1/models" && r.Path != "/v1/messages") {
			t.Errorf("request path %q", r.Path)
		}
	}
}

// Every Anthropic-dialect provider (native key, OAuth subscription, custom) serves
// the SDK's models.retrieve(): the shared allowlist is what carries it.
func TestAnthropicDialectProvidersAllowModelRetrieve(t *testing.T) {
	d := newAnthropicDouble(t)
	for name, allowed := range map[string]func(string, string) bool{
		"anthropic allowlist": anthropicAllow,
		"custom anthropic": func() func(string, string) bool {
			up, _ := newCA(d, types.CustomProvider{}).Upstream()
			return up.Allowed
		}(),
	} {
		if !allowed("GET", "/v1/models/claude-haiku-4-5") || !allowed("GET", "/v1/models/org%2Fmodel") {
			t.Errorf("%s: GET /v1/models/<id> is not allowed", name)
		}
		if allowed("GET", "/v1/models/a/b") || allowed("POST", "/v1/models/claude-haiku-4-5") {
			t.Errorf("%s: retrieve allowlist is too wide", name)
		}
	}
}
