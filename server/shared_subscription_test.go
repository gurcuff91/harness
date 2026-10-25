package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// claudeBackend is "Anthropic" for a Claude Code subscription: it REFUSES any
// request that is not shaped like Claude Code's (token, identity system block,
// billing header) — so passing through it proves the proxy did all of that.
type claudeBackend struct {
	*fakeUpstream
	mu       sync.Mutex
	accepted int
	refused  []string
}

func newClaudeBackend(t *testing.T) *claudeBackend {
	cb := &claudeBackend{}
	cb.fakeUpstream = newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"claude-test-1","display_name":"Claude Test 1","max_input_tokens":777000,"max_tokens":55000,
			 "capabilities":{"image_input":{"supported":true},"thinking":{"supported":true,"types":{"adaptive":{"supported":true},"enabled":{"supported":false}}},
			 "effort":{"low":{"supported":true},"high":{"supported":true}}}}],"has_more":false,"last_id":"claude-test-1"}`)
			return
		}
		var req struct {
			System []struct{ Text string } `json:"system"`
			Tools  []struct{ Name string } `json:"tools"`
		}
		json.Unmarshal(body, &req)
		var why string
		switch {
		case r.Header.Get("Authorization") != "Bearer SHARED-OAUTH-TOKEN":
			why = "not the subscription token: " + r.Header.Get("Authorization")
		case r.Header.Get("X-Api-Key") != "":
			why = "an x-api-key reached the subscription backend"
		case len(req.System) == 0 || !strings.HasPrefix(req.System[0].Text, "You are Claude Code"):
			why = "no Claude Code identity block"
		case r.Header.Get("X-Anthropic-Billing-Header") == "":
			why = "no billing header"
		}
		for _, tl := range req.Tools {
			if tl.Name == "Fetch" { // harness's own name must have been mapped to WebFetch
				why = "tool name was not mapped to Claude Code's"
			}
		}
		cb.mu.Lock()
		defer cb.mu.Unlock()
		if why != "" {
			cb.refused = append(cb.refused, why)
			w.WriteHeader(401)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"`+why+`"}}`)
			return
		}
		cb.accepted++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n")
		if len(req.Tools) > 0 { // answer by calling the tool, with Claude Code's name for it
			io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"WebFetch\",\"input\":{}}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"url\\\":\\\"http://x\\\"}\"}}\n\n")
			io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":4}}\n\n")
		} else {
			io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"shared pong\"}}\n\n")
			io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		}
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	return cb
}

// instanceA is harness instance A: claude-oauth connected (a fake token) and
// served over HTTP. Returns A's address.
func instanceA(t *testing.T, backend *claudeBackend) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	if err := config.GetCredentialsManager().SetCredential("claude-oauth", config.ProviderCredential{
		Type: "oauth", AccessToken: "SHARED-OAUTH-TOKEN", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	oauth, _ := providers.NewClaudeOAuth()
	providers.RedirectForTest(oauth, backend.URL)
	t.Cleanup(providers.SetForTest([]providers.Provider{oauth}))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv.Addr()
}

// The whole point: instance B declares a custom anthropic provider pointing at
// A's claude-oauth proxy — with NO credential — and uses it through a normal
// SESSION (model listing, a turn, a tool call), as if claude-oauth were its own.
func TestSharedSubscriptionAcrossInstances(t *testing.T) {
	backend := newClaudeBackend(t)
	addrA := instanceA(t, backend)

	// Instance B: a registry holding ONLY the custom provider (no claude-oauth).
	proxyRoot := "http://" + addrA + "/api/providers/claude-oauth/proxy"
	if err := agent.NewAnthropicProvider("shared-claude", proxyRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider("shared-claude") })
	bAgent := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { bAgent.Close() })

	// 1. B lists A's models, WITH their capabilities (the proxy passed them verbatim).
	var listed *types.ModelListing
	for _, m := range bAgent.Models() {
		if m.Model == "shared-claude/claude-test-1" {
			m := m
			listed = &m
		}
	}
	if listed == nil {
		t.Fatalf("B does not list A's models: %v", bAgent.Models())
	}
	if listed.Dialect != "anthropic" || listed.ContextWindow != 777000 || listed.MaxTokens != 55000 || !listed.Vision || !listed.ThinkingAdaptive || !listed.EffortLevels["high"] ||
		listed.Sources[types.FieldContextWindow] != types.SourceProvider {
		t.Errorf("model data lost on the way through the proxy: %+v", listed)
	}

	// 2. A real session on B talks to the subscription.
	sess, err := bAgent.NewSession(t.TempDir(), "shared-claude/claude-test-1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.PromptAndWait(context.Background(), "Say pong")
	if err != nil {
		t.Fatalf("turn through B → A → subscription: %v (backend refused: %v)", err, backend.refused)
	}
	if !strings.Contains(out, "shared pong") {
		t.Errorf("reply = %q", out)
	}
	if len(backend.refused) != 0 {
		t.Fatalf("the subscription backend refused: %v — A's proxy must make B's plain request acceptable", backend.refused)
	}

	// 3. B's session tools go through with B's own names; A maps them and maps
	// the model's tool_use back, so B's agent sees (and executes) "Fetch".
	for _, c := range backend.seen {
		if c.Path != "/v1/messages" {
			continue
		}
		var req struct{ Tools []struct{ Name string } }
		json.Unmarshal(c.Body, &req)
		for _, tl := range req.Tools {
			if tl.Name == "Fetch" {
				t.Errorf("tool %q reached the backend unmapped", tl.Name)
			}
		}
	}
	// B never held a credential: nothing of B's carried the token.
	if _, ok := config.GetCredentialsManager().Credential("shared-claude"); ok {
		t.Error("B must have no credential for the shared provider")
	}
}

// The tool-call round trip: the model asks for "WebFetch" (Claude Code's name);
// B's session must receive it as "Fetch" — its own tool — through A's response
// mapping. Driven at the provider level to see the exact tool name B gets.
func TestSharedSubscriptionToolNamesRoundTrip(t *testing.T) {
	backend := newClaudeBackend(t)
	addrA := instanceA(t, backend)
	if err := agent.NewAnthropicProvider("shared-claude-tools", "http://"+addrA+"/api/providers/claude-oauth/proxy"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider("shared-claude-tools") })

	p := providers.Find("shared-claude-tools")
	resp, err := p.CompleteStream(context.Background(), &types.Request{
		Model: "claude-test-1", MaxTokens: 100, Messages: []types.Message{types.NewUserTextMessage("fetch it")},
		Tools: []types.ToolDef{{Name: "Fetch", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatalf("%v (refused: %v)", err, backend.refused)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "Fetch" || !strings.Contains(string(resp.ToolCalls[0].Input), "http://x") {
		t.Fatalf("tool calls = %+v: B must see ITS tool name (Fetch), not Claude Code's (WebFetch)", resp.ToolCalls)
	}
	if len(backend.refused) != 0 {
		t.Errorf("refused: %v", backend.refused)
	}
}

// Chaining: C → B (custom anthropic) → A (claude-oauth). B's own proxy forwards
// to A untouched, and the identity is applied once, at A.
func TestSharedSubscriptionChainOfThree(t *testing.T) {
	backend := newClaudeBackend(t)
	addrA := instanceA(t, backend)
	if err := agent.NewAnthropicProvider("hop-b", "http://"+addrA+"/api/providers/claude-oauth/proxy"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider("hop-b") })
	bAgent := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { bAgent.Close() })
	srvB, err := Start(bAgent, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srvB.Close() })

	// C talks to B's proxy for "hop-b" with a plain Anthropic request.
	resp, body := do(t, "POST", "http://"+srvB.Addr()+"/api/providers/hop-b/proxy/v1/messages",
		`{"model":"claude-test-1","max_tokens":50,"messages":[{"role":"user","content":"Say pong"}]}`, map[string]string{"x-api-key": "whatever"})
	if resp.StatusCode != 200 || !strings.Contains(string(body), "shared pong") {
		t.Fatalf("%d %s (refused: %v)", resp.StatusCode, body, backend.refused)
	}
	if len(backend.refused) != 0 || backend.accepted != 1 {
		t.Errorf("accepted=%d refused=%v", backend.accepted, backend.refused)
	}
	// B's registry entry advertises the dialect for C's discovery.
	c := client.New(srvB.Addr())
	ps, _ := c.GetProviders()
	for _, p := range ps {
		if p.Name == "hop-b" && (p.Dialect != "anthropic" || p.ProxyPath != "/api/providers/hop-b/proxy") {
			t.Errorf("hop-b = %+v", p)
		}
	}
}

// A settings.json-declared anthropic custom provider is picked up by reload and
// served like any other — and a type switch under the same name is honored.
func TestCustomAnthropicSettingsReload(t *testing.T) {
	backend := newClaudeBackend(t)
	addrA := instanceA(t, backend)
	sm := config.GetSettingsManager()
	root := "http://" + addrA + "/api/providers/claude-oauth/proxy"
	if err := sm.SetCustomProvider("decl-claude", config.CustomProvider{Type: "anthropic", URL: root, Display: "Declared"}); err != nil {
		t.Fatal(err)
	}
	if err := agent.ReloadCustomProviders(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sm.DeleteCustomProvider("decl-claude"); agent.ReloadCustomProviders() })
	p := providers.Find("decl-claude")
	if p == nil || p.Dialect() != "anthropic" || p.DisplayName() != "Declared" {
		t.Fatalf("declared provider = %v", p)
	}
	if _, err := p.FetchModels(); err != nil || p.ModelMeta("claude-test-1") == nil {
		t.Fatalf("fetch via settings-declared provider: %v", err)
	}
	// unchanged reload keeps the same object (and its model cache)
	agent.ReloadCustomProviders()
	if providers.Find("decl-claude") != p {
		t.Error("an unchanged entry must keep its provider object across reloads")
	}
	// the same name switched to openai becomes a different provider type
	sm.SetCustomProvider("decl-claude", config.CustomProvider{Type: "openai", URL: root})
	agent.ReloadCustomProviders()
	if q := providers.Find("decl-claude"); q == nil || q == p || q.Dialect() != "openai" {
		t.Errorf("after a type switch: %v", q)
	}
}
