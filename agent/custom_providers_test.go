package agent

import (
	"net/http"
	"net/http/httptest"
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

	if err := NewOpenAIProvider("agent-sdk-proxy", srv.URL, "key",
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

	if err := NewOpenAIProvider("openai", "https://x", "key"); err == nil {
		t.Error("expected an error registering a reserved built-in provider name")
	}
}

func TestProviderWithFetchModels_OverridesDiscovery(t *testing.T) {
	withCleanProviderRegistry(t)

	if err := NewOpenAIProvider("static-proxy", "https://unused.invalid", "key",
		ProviderWithFetchModels(func(apiKey string) ([]types.ModelMeta, error) {
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
