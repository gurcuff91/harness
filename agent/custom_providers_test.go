package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// withCleanProviderRegistry isolates a test from the real
// ~/.harness/settings.json (NewOpenAIProvider ultimately calls
// providers.EnsureRegistry, which reads
// config.GetSettingsManager().CustomProviders()) and restores the global
// provider registry to its pre-test snapshot afterward, so a provider
// registered by one test never leaks into another test in this package's
// binary. Same pattern as internal/providers/register_openai_test.go's
// withCleanRegistry — duplicated here (not exported/shared) since it needs
// to reach into providers.All, an internal/ package this test file already
// legitimately imports directly (same as agent.go itself does).
func withCleanProviderRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	providers.EnsureRegistry()
	snapshot := append([]providers.Provider{}, providers.All...)
	t.Cleanup(func() {
		providers.All = snapshot
	})
}

func TestNewOpenAIProvider_RegistersAndResolvableThroughAgent(t *testing.T) {
	withCleanProviderRegistry(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"agent-visible-model"}]}`))
	}))
	defer srv.Close()

	if err := NewOpenAIProvider("agent-sdk-proxy", srv.URL,
		ProviderWithDisplay("Agent SDK Proxy"),
		ProviderWithHeaders(map[string]string{"X-Test": "1"}),
	); err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	var found *types.ProviderInfo
	for _, p := range a.Providers() {
		if p.Name == "agent-sdk-proxy" {
			pp := p
			found = &pp
			break
		}
	}
	if found == nil {
		t.Fatal("agent-sdk-proxy not found in a.Providers()")
	}
	if found.DisplayName != "Agent SDK Proxy" {
		t.Errorf("DisplayName = %q, want Agent SDK Proxy", found.DisplayName)
	}
	if !found.Active {
		t.Error("Active = false, want true")
	}

	var foundModel bool
	for _, m := range a.Models() {
		if m.Provider == "agent-sdk-proxy" {
			foundModel = true
			if m.Model != "agent-sdk-proxy/agent-visible-model" {
				t.Errorf("Model = %q, want agent-sdk-proxy/agent-visible-model", m.Model)
			}
		}
	}
	if !foundModel {
		t.Error("registered provider's model did not appear in a.Models()")
	}
}

func TestNewOpenAIProvider_RejectsReservedName(t *testing.T) {
	withCleanProviderRegistry(t)

	if err := NewOpenAIProvider("openai", "https://x"); err == nil {
		t.Error("expected an error registering a reserved built-in provider name")
	}
}

func TestProviderWithFetchModels_OverridesDiscovery(t *testing.T) {
	withCleanProviderRegistry(t)

	if err := NewOpenAIProvider("static-proxy", "https://unused.invalid",
		ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
			return []types.ModelMeta{{ID: "static-1"}, {ID: "static-2"}}, nil
		}),
	); err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	count := 0
	for _, m := range a.Models() {
		if m.Provider == "static-proxy" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("got %d models from static-proxy, want 2 (from the ProviderWithFetchModels hook)", count)
	}
}

// TestNewOpenAIProvider_AlwaysActiveNoConnectStep confirms an SDK-registered
// provider is immediately active without any connect step — authentication
// lives entirely in ProviderWithHeaders, there's no apiKey parameter and no
// "not connected" state to resolve.
func TestNewOpenAIProvider_AlwaysActiveNoConnectStep(t *testing.T) {
	withCleanProviderRegistry(t)

	if err := NewOpenAIProvider("always-active-proxy", "https://x",
		ProviderWithHeaders(map[string]string{"X-Api-Key": "secret"}),
	); err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	for _, p := range a.Providers() {
		if p.Name == "always-active-proxy" {
			if !p.Active {
				t.Error("Active = false, want true — no connect step should be needed")
			}
			return
		}
	}
	t.Fatal("always-active-proxy not found in a.Providers()")
}

// TestNewOpenAIProvider_ReasoningSplitReachesTheWire reproduces the real
// incident through the fully PROGRAMMATIC (SDK) registration path — a
// custom provider fronting a MiniMax-compatible gateway (Gus's Kaiban
// deployment) needs "reasoning_split": true on every request to get its
// thinking routed into reasoning_content instead of leaking inline inside
// content as literal "<think>...</think>". Without
// ProviderWithReasoningSplit, this flag never reaches the wire at all
// (proven by TestNewOpenAIProvider_RegistersAndResolvableThroughAgent's
// sibling coverage of the default path via internal/providers' own
// TestCustomOpenAI_ReasoningSplitDefaultsToFalseNotSentOnWire) — this test
// is the additive opt-in half of that story.
func TestNewOpenAIProvider_ReasoningSplitReachesTheWire(t *testing.T) {
	withCleanProviderRegistry(t)

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	if err := NewOpenAIProvider("minimax-gateway", srv.URL,
		ProviderWithReasoningSplit(),
	); err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	var found providers.Provider
	for _, p := range providers.All {
		if p.Name() == "minimax-gateway" {
			found = p
			break
		}
	}
	if found == nil {
		t.Fatal("minimax-gateway not found in providers.All")
	}
	req := &types.Request{Model: "MiniMax-M2", Messages: []types.Message{}, MaxTokens: 10}
	if _, err := found.CompleteStream(context.Background(), req, func(types.StreamEvent) {}); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if !strings.Contains(string(gotBody), `"reasoning_split":true`) {
		t.Errorf("request body = %s, want it to contain \"reasoning_split\":true", gotBody)
	}
}
