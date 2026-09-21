package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// ── DisplayName / ModelsURL fallback ─────────────────────────────────────

func TestCustomOpenAI_DisplayNameFallsBackToName(t *testing.T) {
	o := NewCustomOpenAI("my-proxy", types.CustomProvider{Type: "openai", URL: "https://x"})
	if o.DisplayName() != "my-proxy" {
		t.Errorf("DisplayName() = %q, want the provider name as fallback", o.DisplayName())
	}
	if o.Name() != "my-proxy" {
		t.Errorf("Name() = %q, want my-proxy", o.Name())
	}
}

func TestCustomOpenAI_DisplayNameUsesConfiguredValue(t *testing.T) {
	o := NewCustomOpenAI("my-proxy", types.CustomProvider{Type: "openai", URL: "https://x", Display: "Acme Proxy"})
	if o.DisplayName() != "Acme Proxy" {
		t.Errorf("DisplayName() = %q, want the configured Display", o.DisplayName())
	}
}

// ── Always-active, header-only authentication ─────────────────────────────
//
// The direct regression coverage for the redesign: a custom provider
// (declarative or programmatic — both construct the same CustomOpenAI) has
// no separate API key concept. It's always active, Connect/Disconnect are
// rejected, and CredentialType is CredTypeNone — the exact same shape
// auto-detected Ollama already has, just for a different underlying
// reason (headers vs. a local ping).

func TestCustomOpenAI_AlwaysActiveNoCredentials(t *testing.T) {
	o := NewCustomOpenAI("my-proxy", types.CustomProvider{Type: "openai", URL: "https://x"})
	if !o.IsActive() {
		t.Error("IsActive() = false, want true — a custom provider has no credential to be missing")
	}
	if o.CredentialType() != types.CredTypeNone {
		t.Errorf("CredentialType() = %v, want CredTypeNone", o.CredentialType())
	}
	creds, err := o.ResolveCredentials()
	if err != nil {
		t.Errorf("ResolveCredentials() unexpected error: %v", err)
	}
	if creds.Type != types.CredTypeNone {
		t.Errorf("ResolveCredentials().Type = %v, want CredTypeNone", creds.Type)
	}
}

func TestCustomOpenAI_ConnectAndDisconnectAreRejected(t *testing.T) {
	o := NewCustomOpenAI("my-proxy", types.CustomProvider{Type: "openai", URL: "https://x"})
	if err := o.Connect(types.Credentials{Type: types.CredTypeAPIKey, APIKey: "whatever"}); err == nil {
		t.Error("Connect() must be rejected — authentication is header-only, harness connect doesn't apply")
	}
	if err := o.Disconnect(); err == nil {
		t.Error("Disconnect() must be rejected for the same reason")
	}
}

// ── fetchCustomOpenAIModels ──────────────────────────────────────────────

// TestFetchCustomOpenAIModels_AcceptsAllModelsRegardlessOfName is the
// direct regression test for the exact failure mode Gus flagged live: a
// custom provider fronting a non-OpenAI backend (MiniMax, Llama, DeepSeek,
// arbitrary names) must return every listed model, NOT be filtered through
// OpenAI's own isOpenAIChatModel prefix rule (gpt-/o1/o3/o4/chatgpt-) — if
// that filter ever leaked into this path, a proxy like the one below would
// silently return zero models.
func TestFetchCustomOpenAIModels_AcceptsAllModelsRegardlessOfName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"id":"minimax-m3"},
			{"id":"llama-3.1-70b"},
			{"id":"deepseek-chat"},
			{"id":"mixtral-8x7b-instruct"}
		]}`))
	}))
	defer srv.Close()

	metas, err := fetchCustomOpenAIModels("my-proxy", srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metas) != 4 {
		t.Fatalf("got %d models, want 4 (no OpenAI-prefix filtering should apply) — got: %+v", len(metas), metas)
	}
	ids := map[string]bool{}
	for _, m := range metas {
		ids[m.ID] = true
	}
	for _, want := range []string{"minimax-m3", "llama-3.1-70b", "deepseek-chat", "mixtral-8x7b-instruct"} {
		if !ids[want] {
			t.Errorf("expected model %q in results, got %v", want, ids)
		}
	}
}

// TestFetchCustomOpenAIModels_UsesModelsURLWhenSet confirms the ModelsURL
// override is actually used instead of the <baseURL>/models fallback.
func TestFetchCustomOpenAIModels_UsesModelsURLWhenSet(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		w.Write([]byte(`{"data":[{"id":"some-model"}]}`))
	}))
	defer srv.Close()

	_, err := fetchCustomOpenAIModels("my-proxy", srv.URL, srv.URL+"/custom-models-listing", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hitPath != "/custom-models-listing" {
		t.Errorf("hit path = %q, want /custom-models-listing (ModelsURL should override the <url>/models fallback)", hitPath)
	}
}

// TestFetchCustomOpenAIModels_FallsBackToURLSlashModels confirms the
// documented fallback when ModelsURL is unset.
func TestFetchCustomOpenAIModels_FallsBackToURLSlashModels(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if _, err := fetchCustomOpenAIModels("my-proxy", srv.URL, "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hitPath != "/models" {
		t.Errorf("hit path = %q, want /models (the <url>/models fallback)", hitPath)
	}
}

// TestFetchCustomOpenAIModels_SendsConfiguredHeaders confirms extra headers
// reach the models-listing request, AND that no automatic "Authorization:
// Bearer" is ever sent — auth is entirely whatever Headers declares.
func TestFetchCustomOpenAIModels_SendsConfiguredHeaders(t *testing.T) {
	var gotHeader, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Org-Id")
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := fetchCustomOpenAIModels("my-proxy", srv.URL, "", map[string]string{"X-Org-Id": "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "acme" {
		t.Errorf("X-Org-Id header = %q, want acme", gotHeader)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty (no automatic Bearer header)", gotAuth)
	}
}

// TestFetchCustomOpenAIModels_HeadersCarryCustomAuth is the direct
// regression test for the field report: a gateway authenticating via a
// custom header (X-Api-Key), not Authorization — must reach the request
// exactly as configured.
func TestFetchCustomOpenAIModels_HeadersCarryCustomAuth(t *testing.T) {
	var gotAPIKey, gotActor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-Api-Key")
		gotActor = r.Header.Get("X-Actor")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	headers := map[string]string{
		"X-Api-Key": "kb_t_cm_secret",
		"X-Actor":   "agent:overbooking-scoring-optimizer",
	}
	if _, err := fetchCustomOpenAIModels("my-proxy", srv.URL, "", headers); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAPIKey != "kb_t_cm_secret" {
		t.Errorf("X-Api-Key = %q, want kb_t_cm_secret", gotAPIKey)
	}
	if gotActor != "agent:overbooking-scoring-optimizer" {
		t.Errorf("X-Actor = %q, want agent:overbooking-scoring-optimizer", gotActor)
	}
}

func TestFetchCustomOpenAIModels_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := fetchCustomOpenAIModels("my-proxy", srv.URL, "", nil)
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

// ── CompleteStream header wiring ─────────────────────────────────────────

// TestCustomOpenAI_CompleteStreamSendsConfiguredHeadersNoAutoAuth confirms
// the extra headers configured on the custom provider reach the
// chat-completions request via llm.DoOpenAIStream's extraHeaders param,
// and that no automatic "Authorization: Bearer" is sent (there's no apiKey
// anymore — CompleteStream always passes "" as the apiKey argument).
func TestCustomOpenAI_CompleteStreamSendsConfiguredHeadersNoAutoAuth(t *testing.T) {
	var gotHeader, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Org-Id")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		// Minimal valid SSE completion so DoOpenAIStream doesn't hang/error
		// past the header check this test cares about.
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	o := NewCustomOpenAI("my-proxy", types.CustomProvider{
		Type:    "openai",
		URL:     srv.URL,
		Headers: map[string]string{"X-Org-Id": "acme"},
	})

	req := &types.Request{Model: "some-model", Messages: []types.Message{}, MaxTokens: 10}
	_, _ = o.CompleteStream(context.Background(), req, func(types.StreamEvent) {})

	if gotHeader != "acme" {
		t.Errorf("X-Org-Id header on CompleteStream = %q, want acme", gotHeader)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty (no automatic Bearer header)", gotAuth)
	}
}

// TestCustomOpenAI_CompleteStreamAcceptsCleanEOFAfterTerminalChunk
// reproduces a real incident: a gateway proxying to MiniMax (Gus's Kaiban
// deployment) closes the SSE response right after a chunk carrying
// finish_reason, without ever sending the OpenAI-dialect "[DONE]"
// sentinel — the same behavior internal/providers/minimax.go's own
// CompleteStream already works around via AllowCleanEOF (see commit
// b3f42d7, "fix: accept MiniMax clean SSE completion"). Before this test
// existed, CustomOpenAI.CompleteStream never set AllowCleanEOF, so any
// custom provider proxying to (or otherwise behaving like) MiniMax would
// surface "stream ended unexpectedly before completion (no [DONE] marker
// received)" on every single turn even though the model's answer arrived
// completely intact.
func TestCustomOpenAI_CompleteStreamAcceptsCleanEOFAfterTerminalChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// A terminal chunk (finish_reason set) followed by a clean EOF —
		// no "data: [DONE]\n\n" line at all.
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"pong"},"finish_reason":"stop","index":0}]}` + "\n\n"))
	}))
	defer srv.Close()

	o := NewCustomOpenAI("my-proxy", types.CustomProvider{Type: "openai", URL: srv.URL})
	req := &types.Request{Model: "some-model", Messages: []types.Message{}, MaxTokens: 10}
	resp, err := o.CompleteStream(context.Background(), req, func(types.StreamEvent) {})
	if err != nil {
		t.Fatalf("expected no error for a clean EOF after a terminal chunk, got: %v", err)
	}
	if resp.Text != "pong" {
		t.Errorf("resp.Text = %q, want %q", resp.Text, "pong")
	}
}

// TestCustomOpenAI_CompleteStreamStillRejectsCleanEOFWithoutTerminalChunk
// confirms AllowCleanEOF didn't turn into a blanket "ignore dropped
// connections" — a stream that stops with NO terminal (finish_reason)
// chunk and no [DONE] must still surface as a real error, exactly as
// before this change.
func TestCustomOpenAI_CompleteStreamStillRejectsCleanEOFWithoutTerminalChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// A non-terminal delta chunk, then the connection just closes —
		// simulating a genuine mid-response drop.
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"partial"},"index":0}]}` + "\n\n"))
	}))
	defer srv.Close()

	o := NewCustomOpenAI("my-proxy", types.CustomProvider{Type: "openai", URL: srv.URL})
	req := &types.Request{Model: "some-model", Messages: []types.Message{}, MaxTokens: 10}
	_, err := o.CompleteStream(context.Background(), req, func(types.StreamEvent) {})
	if err == nil {
		t.Fatal("expected an error for a dropped connection with no terminal chunk and no [DONE]")
	}
}

// ── JSON shape sanity ────────────────────────────────────────────────────

func TestFetchCustomOpenAIModels_ParsesDataShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = map[string]any{"data": []map[string]string{{"id": "model-a"}}}
		b, _ := json.Marshal(body)
		w.Write(b)
	}))
	defer srv.Close()

	metas, err := fetchCustomOpenAIModels("my-proxy", srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != "model-a" {
		t.Errorf("unexpected metas: %+v", metas)
	}
}
