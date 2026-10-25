package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// redirectTransport sends every request to target, whatever host it names —
// lets a provider with a hardcoded https://api.anthropic.com URL talk to a fake.
type redirectTransport struct{ target *url.URL }

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// oauthFake is a claude-oauth provider with valid (unexpired) fake credentials
// whose HTTP client lands on a recording fake instead of Anthropic.
type captured struct {
	Path   string
	Header http.Header
	Body   []byte
}

func newClaudeOAuthFake(t *testing.T) (*ClaudeOAuth, *captured) {
	t.Helper()
	creds := configstore.NewInMemoryStore()
	t.Cleanup(config.SwapStoresForTest(nil, creds))
	if err := config.GetCredentialsManager().SetCredential("claude-oauth", config.ProviderCredential{
		Type: "oauth", AccessToken: "fake-access-token", RefreshToken: "fake-refresh",
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	got := &captured{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path, got.Header = r.URL.Path, r.Header.Clone()
		got.Body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(fake.Close)
	u, _ := url.Parse(fake.URL)
	c, err := NewClaudeOAuth()
	if err != nil {
		t.Fatal(err)
	}
	c.client = &http.Client{Transport: redirectTransport{u}}
	return c, got
}

// The proxy rewrite must put on the wire what a SESSION request (CompleteStream)
// puts there for the same conversation: same system blocks, same tool names,
// same billing header. Compared field by field against the real code path.
func TestClaudeOAuthProxyRewriteMatchesCompleteStream(t *testing.T) {
	c, sess := newClaudeOAuthFake(t)

	req := &types.Request{
		Model:        "claude-sonnet-5",
		SystemPrompt: "Be terse.",
		MaxTokens:    1000,
		Messages: []types.Message{
			types.NewUserTextMessage("fetch the page"),
			{Role: types.RoleAssistant, Parts: []types.ContentPart{{ToolCall: &types.ToolCall{ID: "t1", Name: "Fetch", Input: json.RawMessage(`{"url":"x"}`)}}}},
			{Role: types.RoleUser, Parts: []types.ContentPart{{ToolResult: &types.ToolResult{ID: "t1", Output: "page"}}}},
			types.NewUserTextMessage("thanks"),
		},
		Tools: []types.ToolDef{
			{Name: "Fetch", Description: "fetch", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "Bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "KaibanAPI", Description: "kb", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		ThinkingLevel: "off",
	}
	if _, err := c.CompleteStream(context.Background(), req, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	var want map[string]any
	json.Unmarshal(sess.Body, &want)
	wantHeaders := sess.Header.Clone()

	// The same conversation, written the way an Anthropic SDK client writes it
	// (harness's own tool names, plain Messages request).
	client := `{"model":"claude-sonnet-5","max_tokens":1000,"system":"Be terse.",
	 "messages":[
	  {"role":"user","content":"fetch the page"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Fetch","input":{"url":"x"}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"page"}]},
	  {"role":"user","content":"thanks"}],
	 "tools":[{"name":"Fetch","description":"fetch","input_schema":{"type":"object"}},
	          {"name":"Bash","description":"run","input_schema":{"type":"object"}},
	          {"name":"KaibanAPI","description":"kb","input_schema":{"type":"object"}}],
	 "stream":true}`
	up, err := c.Upstream()
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{}
	for k, v := range up.Headers {
		headers[k] = v
	}
	out, err := up.Rewrite("/v1/messages", []byte(client), headers)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}

	// system: identity first, then the caller's text — block for block.
	gs, ws := got["system"].([]any), want["system"].([]any)
	if len(gs) != len(ws) {
		t.Fatalf("system blocks: got %d, want %d (%v)", len(gs), len(ws), gs)
	}
	for i := range gs {
		if gs[i].(map[string]any)["text"] != ws[i].(map[string]any)["text"] {
			t.Errorf("system[%d] text = %v, want %v", i, gs[i].(map[string]any)["text"], ws[i].(map[string]any)["text"])
		}
	}
	// tool names exactly as a session sends them.
	names := func(m map[string]any) []string {
		var n []string
		for _, t := range m["tools"].([]any) {
			n = append(n, t.(map[string]any)["name"].(string))
		}
		return n
	}
	if !reflect.DeepEqual(names(got), names(want)) {
		t.Errorf("tool names = %v, want %v", names(got), names(want))
	}
	// the tool_use in the assistant turn is mapped the same way.
	toolUse := func(m map[string]any) string {
		for _, msg := range m["messages"].([]any) {
			c, ok := msg.(map[string]any)["content"].([]any)
			if !ok {
				continue
			}
			for _, p := range c {
				if pm := p.(map[string]any); pm["type"] == "tool_use" {
					return pm["name"].(string)
				}
			}
		}
		return ""
	}
	if toolUse(got) != toolUse(want) || toolUse(got) != "WebFetch" {
		t.Errorf("tool_use name = %q, want %q (WebFetch)", toolUse(got), toolUse(want))
	}
	// the billing header derives from the first user message — identical.
	if headers["x-anthropic-billing-header"] == "" || headers["x-anthropic-billing-header"] != wantHeaders.Get("x-anthropic-billing-header") {
		t.Errorf("billing header = %q, want %q", headers["x-anthropic-billing-header"], wantHeaders.Get("x-anthropic-billing-header"))
	}
	// identity headers are the session's: every header CompleteStream sent
	// (token, beta, x-app, user-agent…) is what the proxy would send, except
	// the two that are per-request by design (a fresh request id).
	sent := http.Header{}
	for k, v := range headers {
		sent.Set(k, v)
	}
	for k := range wantHeaders {
		switch http.CanonicalHeaderKey(k) {
		case "X-Client-Request-Id", "Content-Length", "Content-Type", "Accept-Encoding", "Anthropic-Version", "X-Stainless-Retry-Count":
			continue
		}
		if sent.Get(k) != wantHeaders.Get(k) {
			t.Errorf("header %s = %q, session sent %q", k, sent.Get(k), wantHeaders.Get(k))
		}
	}
	if sent.Get("Authorization") != "Bearer fake-access-token" {
		t.Errorf("Authorization = %q", sent.Get("Authorization"))
	}
	// untouched fields pass through.
	if got["model"] != "claude-sonnet-5" || got["max_tokens"] != float64(1000) || got["stream"] != true {
		t.Errorf("pass-through fields altered: %v", got)
	}
}

func TestClaudeOAuthRewriteSystemShapes(t *testing.T) {
	cases := map[string]struct {
		system     string // JSON of the caller's system field, "" = absent
		wantBlocks int
		wantLast   string
	}{
		"absent":    {"", 1, claudeCodeIdentity},
		"empty":     {`""`, 1, claudeCodeIdentity},
		"null":      {`null`, 1, claudeCodeIdentity},
		"string":    {`"be terse"`, 2, "be terse"},
		"blocks":    {`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, 3, "b"},
		"one block": {`[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]`, 2, "a"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]`
			if tc.system != "" {
				body += `,"system":` + tc.system
			}
			body += `}`
			out, err := rewriteClaudeOAuthBody("/v1/messages", []byte(body), map[string]string{})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				System []map[string]any `json:"system"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.System) != tc.wantBlocks {
				t.Fatalf("system blocks = %d, want %d: %s", len(got.System), tc.wantBlocks, out)
			}
			if got.System[0]["text"] != claudeCodeIdentity {
				t.Errorf("first block is not the identity: %v", got.System[0])
			}
			if got.System[len(got.System)-1]["text"] != tc.wantLast {
				t.Errorf("last block = %v, want %q", got.System[len(got.System)-1]["text"], tc.wantLast)
			}
		})
	}
}

func TestClaudeOAuthRewriteToolChoiceAndTypes(t *testing.T) {
	body := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],
	 "tools":[{"name":"Fetch","input_schema":{}},{"name":"mcp__srv__x","input_schema":{}},{"name":"Zed","input_schema":{}}],
	 "tool_choice":{"type":"tool","name":"Fetch"}}`
	out, err := rewriteClaudeOAuthBody("/v1/messages", []byte(body), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools      []struct{ Name string } `json:"tools"`
		ToolChoice struct{ Name string }   `json:"tool_choice"`
	}
	json.Unmarshal(out, &got)
	want := []string{"WebFetch", "mcp__srv__x", "mcp__x__Zed"}
	for i, tl := range got.Tools {
		if tl.Name != want[i] {
			t.Errorf("tools[%d] = %q, want %q", i, tl.Name, want[i])
		}
	}
	if got.ToolChoice.Name != "WebFetch" {
		t.Errorf("tool_choice.name = %q, want WebFetch", got.ToolChoice.Name)
	}
}

// Text the caller sent must reach the model byte-identical: the rewrite never
// re-encodes what it doesn't touch, so characters json.Marshal would escape
// (<, >, &) stay exactly as written, and so does any escape the caller used.
func TestClaudeOAuthRewritePreservesText(t *testing.T) {
	const text = `a < b && c > d — ünï \"q\" end`
	for name, raw := range map[string]string{
		"raw characters": text,
		"caller escaped": `a \u003c b \u0026\u0026 c \u003e d`,
	} {
		t.Run(name, func(t *testing.T) {
			in := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"` + raw + `"}]}`
			out, err := rewriteClaudeOAuthBody("/v1/messages", []byte(in), map[string]string{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), `"content":"`+raw+`"`) {
				t.Errorf("content was re-encoded:\n in  %s\n out %s", in, out)
			}
			// and the engine's own text (system identity) isn't HTML-escaped either
			if !strings.Contains(string(out), "Anthropic's official CLI") {
				t.Errorf("identity block altered: %s", out)
			}
		})
	}
}

func TestClaudeOAuthRewriteBetaForLegacyThinking(t *testing.T) {
	h := map[string]string{"anthropic-beta": "claude-code-20250219"}
	rewriteClaudeOAuthBody("/v1/messages", []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled","budget_tokens":1024}}`), h)
	if !strings.Contains(h["anthropic-beta"], "interleaved-thinking-2025-05-14") {
		t.Errorf("legacy thinking should add the interleaved beta: %q", h["anthropic-beta"])
	}
	h = map[string]string{"anthropic-beta": "claude-code-20250219"}
	rewriteClaudeOAuthBody("/v1/messages", []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"thinking":{"type":"adaptive"}}`), h)
	if strings.Contains(h["anthropic-beta"], "interleaved") {
		t.Errorf("adaptive thinking must not add it: %q", h["anthropic-beta"])
	}
}

func TestClaudeOAuthRewriteRejectsMalformed(t *testing.T) {
	for _, b := range []string{`not json`, `[]`, `{"messages":"x"}`, `{"system":5,"messages":[]}`, `{"tools":"x","messages":[]}`} {
		if _, err := rewriteClaudeOAuthBody("/v1/messages", []byte(b), map[string]string{}); err == nil {
			t.Errorf("body %q: want an error", b)
		}
	}
	// other paths are untouched
	if out, err := rewriteClaudeOAuthBody("/v1/models", []byte("anything"), nil); err != nil || string(out) != "anything" {
		t.Errorf("/v1/models must pass through: %v %q", err, out)
	}
}

// Response side: Claude Code's tool names go back to the caller's — in a stream
// and in a plain message — and nothing else changes.
func TestUnmapClaudeOAuthResponse(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"stream tool_use WebFetch",
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"WebFetch","input":{}}}`,
			`{"content_block":{"id":"t","input":{},"name":"Fetch","type":"tool_use"},"index":1,"type":"content_block_start"}`},
		{"stream tool_use extension",
			`{"type":"content_block_start","content_block":{"type":"tool_use","id":"t","name":"mcp__x__KaibanAPI"}}`,
			`{"content_block":{"id":"t","name":"KaibanAPI","type":"tool_use"},"type":"content_block_start"}`},
		{"text delta untouched",
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"WebFetch tool_use"}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"WebFetch tool_use"}}`},
		{"tool already native",
			`{"type":"content_block_start","content_block":{"type":"tool_use","id":"t","name":"Bash"}}`,
			`{"type":"content_block_start","content_block":{"type":"tool_use","id":"t","name":"Bash"}}`},
		{"non-stream message",
			`{"id":"m","content":[{"type":"text","text":"x"},{"type":"tool_use","id":"t","name":"WebFetch","input":{}}]}`,
			`{"content":[{"text":"x","type":"text"},{"id":"t","input":{},"name":"Fetch","type":"tool_use"}],"id":"m"}`},
		{"not json", `tool_use garbage`, `tool_use garbage`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(unmapClaudeOAuthResponse([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// Every provider hands back what the proxy needs, with the right allowlist.
func TestUpstreamAllowlists(t *testing.T) {
	a := allowList("POST /v1/messages", "GET /v1/models", "GET /x/*", "GET /v1/models/:id")
	for _, tc := range []struct {
		m, p string
		ok   bool
	}{
		{"POST", "/v1/messages", true}, {"GET", "/v1/models", true},
		{"GET", "/v1/messages", false}, {"POST", "/v1/models", false},
		{"POST", "/v1/messages/count_tokens", false},
		{"GET", "/x/a/b", true}, {"GET", "/x", false}, {"DELETE", "/v1/models", false},
		// "/:id" = exactly one non-empty segment
		{"GET", "/v1/models/claude-x", true}, {"GET", "/v1/models/gemma3:4b", true},
		{"GET", "/v1/models/org%2Fmodel", true}, // encoded slash stays ONE segment
		{"GET", "/v1/models/", false}, {"GET", "/v1/models/a/b", false},
		{"POST", "/v1/models/claude-x", false}, {"DELETE", "/v1/models/claude-x", false},
	} {
		if a(tc.m, tc.p) != tc.ok {
			t.Errorf("%s %s = %v, want %v", tc.m, tc.p, !tc.ok, tc.ok)
		}
	}
}

func TestCleanProxyPath(t *testing.T) {
	for p, want := range map[string]string{
		"/v1/messages": "/v1/messages", "/a/b": "/a/b",
		"": "", "v1": "", "/a/../b": "", "/a/./b": "", "/a/%2e%2e/b": "", "/a%2fb": "",
		"/a\\b": "", "/a%5cb": "", "/a%00": "",
		// an encoded slash is allowed ONLY as the id in …/models/<id>
		"/models/org%2Fmodel": "/models/org%2Fmodel", "/v1/models/org%2Fmodel:7b": "/v1/models/org%2Fmodel:7b",
		"/models/a%2Fb%2Fc": "/models/a%2Fb%2Fc", "/models/gemma3:4b": "/models/gemma3:4b",
		"/models/..%2Fx": "", "/models/%2e%2e%2fx": "", "/models/a%2F%2Fb": "", "/models/%2Fa": "", "/models/a%2F": "",
		"/models/a%2F.": "", "/models/a%2F..": "",
		"/chat%2Fcompletions": "", "/v1/chat%2Fcompletions": "", "/other/a%2Fb": "",
		"/models/x/a%2Fb": "", "/a/b/models/a%2Fb": "", "/v2/models/a%2Fb": "", "/models/a%5Cb": "",
	} {
		if got := CleanProxyPath(p); got != want {
			t.Errorf("CleanProxyPath(%q) = %q, want %q", p, got, want)
		}
	}
}

// ── reasoning_split injection ────────────────────────────────────────────

func TestInjectReasoningSplit(t *testing.T) {
	const path = "/chat/completions"
	cases := []struct{ name, in, want string }{
		{"added to an ordinary request",
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			`{"reasoning_split":true,"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"every other byte untouched (spacing, escapes, order)",
			"{ \"model\" :\"m\",\n  \"messages\":[{\"role\":\"user\",\"content\":\"a < b \\u0026 <think>\"}] }",
			"{\"reasoning_split\":true, \"model\" :\"m\",\n  \"messages\":[{\"role\":\"user\",\"content\":\"a < b \\u0026 <think>\"}] }"},
		{"leading whitespace before the brace", "  \n{\"model\":\"m\"}", "  \n{\"reasoning_split\":true,\"model\":\"m\"}"},
		{"empty object", `{}`, `{"reasoning_split":true}`},
		{"empty object with whitespace", "{ \n}", "{\"reasoning_split\":true \n}"},
		{"caller says true: left alone", `{"model":"m","reasoning_split":true}`, `{"model":"m","reasoning_split":true}`},
		{"caller says false: the caller decides", `{"model":"m","reasoning_split":false}`, `{"model":"m","reasoning_split":false}`},
		{"caller says null: still the caller's", `{"model":"m","reasoning_split":null}`, `{"model":"m","reasoning_split":null}`},
		{"the word inside a string is not the field",
			`{"model":"m","messages":[{"role":"user","content":"set \"reasoning_split\":false please"}]}`,
			`{"reasoning_split":true,"model":"m","messages":[{"role":"user","content":"set \"reasoning_split\":false please"}]}`},
		{"nested key of that name is not the top-level field",
			`{"model":"m","metadata":{"reasoning_split":false}}`,
			`{"reasoning_split":true,"model":"m","metadata":{"reasoning_split":false}}`},
		{"not JSON: passed through", `not json`, `not json`},
		{"JSON array: passed through", `[1,2]`, `[1,2]`},
		{"JSON null: passed through", `null`, `null`},
		{"empty body: passed through", ``, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := injectReasoningSplit(path, []byte(tc.in), nil)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("\n got %q\nwant %q", got, tc.want)
			}
			// whatever comes out of a valid object stays valid JSON
			if json.Valid([]byte(tc.in)) {
				if !json.Valid(got) {
					t.Errorf("output is not valid JSON: %q", got)
				}
			}
		})
	}
	// only chat completions: /models and anything else is untouched.
	for _, p := range []string{"/models", "/embeddings"} {
		if got, _ := injectReasoningSplit(p, []byte(`{"a":1}`), nil); string(got) != `{"a":1}` {
			t.Errorf("%s was rewritten: %s", p, got)
		}
	}
}

// The injected flag is a real boolean the backend parses, and the rest of the
// document is unchanged semantically.
func TestInjectReasoningSplitSemantics(t *testing.T) {
	in := `{"model":"MiniMax-M3","stream":true,"max_tokens":50,"messages":[{"role":"user","content":"Say pong"}],"temperature":0.2}`
	out, _ := injectReasoningSplit("/chat/completions", []byte(in), nil)
	var a, b map[string]any
	json.Unmarshal([]byte(in), &a)
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatal(err)
	}
	if b["reasoning_split"] != true {
		t.Errorf("reasoning_split = %v, want true", b["reasoning_split"])
	}
	delete(b, "reasoning_split")
	if !reflect.DeepEqual(a, b) {
		t.Errorf("document changed beyond the flag:\n in  %v\n out %v", a, b)
	}
}

// Which providers inject — and that the proxy sends what a SESSION sends.
func TestReasoningSplitUpstreamPerProvider(t *testing.T) {
	has := func(u Upstream) bool { return u.Rewrite != nil }

	// MiniMax: always.
	mm := NewMiniMax()
	mm.apiKey = "k"
	up, err := mm.Upstream()
	if err != nil || !has(up) {
		t.Fatalf("minimax must inject: err=%v rewrite=%v", err, has(up))
	}
	// Custom, declared with reasoning_split: yes. Without: untouched.
	with := NewCustomOpenAI("rs-yes", types.CustomProvider{Type: "openai", URL: "http://x/v1", ReasoningSplit: true})
	without := NewCustomOpenAI("rs-no", types.CustomProvider{Type: "openai", URL: "http://x/v1"})
	if up, _ := with.Upstream(); !has(up) {
		t.Error("a custom provider declared with reasoning_split must inject")
	}
	if up, _ := without.Upstream(); has(up) {
		t.Error("a custom provider WITHOUT reasoning_split must be forwarded untouched")
	}
	// Other OpenAI-style providers are untouched.
	for name, p := range map[string]Provider{"openai": NewOpenAI(), "ollama-cloud": NewOllamaCloud(), "opencode-go": NewOpenCodeGo()} {
		switch v := p.(type) {
		case *OpenAI:
			v.apiKey = "k"
		case *OllamaCloud:
			v.apiKey = "k"
		case *OpenCodeGo:
			v.apiKey = "k"
		}
		if up, err := p.Upstream(); err != nil || has(up) {
			t.Errorf("%s: err=%v rewrite=%v, want a plain passthrough", name, err, has(up))
		}
	}
}

// The wire body the proxy produces carries the same flag CompleteStream puts on
// a session request to the same provider.
func TestMiniMaxProxyBodyMatchesSessionRequest(t *testing.T) {
	var sess map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sess)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer fake.Close()
	mm := NewMiniMax()
	mm.apiKey = "k"
	RedirectForTest(mm, fake.URL)
	mm.CompleteStream(context.Background(), &types.Request{Model: "MiniMax-M3", MaxTokens: 10, Messages: []types.Message{types.NewUserTextMessage("Say pong")}}, nil)
	if sess["reasoning_split"] != true {
		t.Fatalf("test premise broken: a session request has reasoning_split=%v", sess["reasoning_split"])
	}
	up, _ := mm.Upstream()
	out, err := up.Rewrite("/chat/completions", []byte(`{"model":"MiniMax-M3","max_tokens":10,"messages":[{"role":"user","content":"Say pong"}]}`), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	var proxied map[string]any
	json.Unmarshal(out, &proxied)
	if proxied["reasoning_split"] != sess["reasoning_split"] {
		t.Errorf("proxy sends reasoning_split=%v, a session sends %v", proxied["reasoning_split"], sess["reasoning_split"])
	}
}
