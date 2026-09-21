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

	metas, err := fetchCustomOpenAIModels("my-proxy", "key", srv.URL, "", nil)
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

	_, err := fetchCustomOpenAIModels("my-proxy", "key", srv.URL, srv.URL+"/custom-models-listing", nil)
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

	if _, err := fetchCustomOpenAIModels("my-proxy", "key", srv.URL, "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hitPath != "/models" {
		t.Errorf("hit path = %q, want /models (the <url>/models fallback)", hitPath)
	}
}

// TestFetchCustomOpenAIModels_SendsConfiguredHeaders confirms extra headers
// reach the models-listing request (not just chat completions).
func TestFetchCustomOpenAIModels_SendsConfiguredHeaders(t *testing.T) {
	var gotHeader, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Org-Id")
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := fetchCustomOpenAIModels("my-proxy", "secret-key", srv.URL, "", map[string]string{"X-Org-Id": "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "acme" {
		t.Errorf("X-Org-Id header = %q, want acme", gotHeader)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization header = %q, want \"Bearer secret-key\"", gotAuth)
	}
}

// TestFetchCustomOpenAIModels_InvalidAPIKey confirms a 401/403 maps to a
// clear "invalid API key" error, same as the built-in OpenAI provider.
func TestFetchCustomOpenAIModels_InvalidAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := fetchCustomOpenAIModels("my-proxy", "bad-key", srv.URL, "", nil)
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

// ── CompleteStream header wiring ─────────────────────────────────────────

// TestCustomOpenAI_CompleteStreamSendsConfiguredHeaders confirms the extra
// headers configured on the custom provider actually reach the
// chat-completions request via llm.DoOpenAIStream's extraHeaders param —
// not just the models-listing path.
func TestCustomOpenAI_CompleteStreamSendsConfiguredHeaders(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Org-Id")
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
	o.apiKey = "key"

	req := &types.Request{Model: "some-model", Messages: []types.Message{}, MaxTokens: 10}
	_, _ = o.CompleteStream(context.Background(), req, func(types.StreamEvent) {})

	if gotHeader != "acme" {
		t.Errorf("X-Org-Id header on CompleteStream = %q, want acme", gotHeader)
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

	metas, err := fetchCustomOpenAIModels("my-proxy", "key", srv.URL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != "model-a" {
		t.Errorf("unexpected metas: %+v", metas)
	}
}
