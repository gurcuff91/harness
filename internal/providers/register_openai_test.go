package providers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// withCleanRegistry isolates a test from the real ~/.harness/settings.json
// (RegisterOpenAI calls EnsureRegistry, which reads
// config.GetSettingsManager().CustomProviders(); see
// server/create_session_thinking_test.go for the same GetSettingsManager
// singleton caveat) and restores All to its pre-test snapshot afterward, so
// a provider registered by one test never leaks into another test running
// later in the same binary.
func withCleanRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	registryMu.Lock()
	snapshot := append([]Provider{}, All...)
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		All = snapshot
		registryMu.Unlock()
	})
}

func TestRegisterOpenAI_AppearsInAllWithCorrectFields(t *testing.T) {
	withCleanRegistry(t)

	if err := RegisterOpenAI("sdk-proxy", "https://my-proxy.internal/v1", "SDK Proxy", map[string]string{"X-Api-Key": "secret"}, nil); err != nil {
		t.Fatalf("RegisterOpenAI: %v", err)
	}

	var found Provider
	for _, p := range All {
		if p.Name() == "sdk-proxy" {
			found = p
			break
		}
	}
	if found == nil {
		t.Fatal("sdk-proxy not found in All after RegisterOpenAI")
	}
	if found.DisplayName() != "SDK Proxy" {
		t.Errorf("DisplayName() = %q, want SDK Proxy", found.DisplayName())
	}
	if !found.IsActive() {
		t.Error("IsActive() = false, want true — a registered custom provider is always active (header-only auth)")
	}
	if found.CredentialType() != types.CredTypeNone {
		t.Errorf("CredentialType() = %v, want CredTypeNone", found.CredentialType())
	}
}

func TestRegisterOpenAI_RejectsReservedName(t *testing.T) {
	withCleanRegistry(t)

	for _, name := range []string{"anthropic", "openai", "minimax", "ollama", "ollama-cloud", "opencode-go", "claude-oauth", "codex-oauth"} {
		if err := RegisterOpenAI(name, "https://x", "", nil, nil); err == nil {
			t.Errorf("%s: expected rejection as a reserved built-in name, got nil error", name)
		}
	}
}

func TestRegisterOpenAI_RejectsDuplicateName(t *testing.T) {
	withCleanRegistry(t)

	if err := RegisterOpenAI("dup-proxy", "https://a", "", nil, nil); err != nil {
		t.Fatalf("first RegisterOpenAI: %v", err)
	}
	if err := RegisterOpenAI("dup-proxy", "https://b", "", nil, nil); err == nil {
		t.Error("expected an error registering the same name twice")
	}
}

func TestRegisterOpenAI_DisplayFallsBackToName(t *testing.T) {
	withCleanRegistry(t)

	if err := RegisterOpenAI("no-display-proxy", "https://x", "", nil, nil); err != nil {
		t.Fatalf("RegisterOpenAI: %v", err)
	}
	for _, p := range All {
		if p.Name() == "no-display-proxy" {
			if p.DisplayName() != "no-display-proxy" {
				t.Errorf("DisplayName() = %q, want the name as fallback", p.DisplayName())
			}
			return
		}
	}
	t.Fatal("provider not found")
}

// TestRegisterOpenAI_ConnectRejected confirms harness connect does not
// apply to an SDK-registered provider either — same rejection as the
// settings.json path (see custom_openai_test.go's
// TestCustomOpenAI_ConnectAndDisconnectAreRejected), reached here through
// the actual RegisterOpenAI construction path.
func TestRegisterOpenAI_ConnectRejected(t *testing.T) {
	withCleanRegistry(t)

	if err := RegisterOpenAI("connect-rejected-proxy", "https://x", "", nil, nil); err != nil {
		t.Fatalf("RegisterOpenAI: %v", err)
	}
	for _, p := range All {
		if p.Name() == "connect-rejected-proxy" {
			if err := p.Connect(types.Credentials{Type: types.CredTypeAPIKey, APIKey: "whatever"}); err == nil {
				t.Error("Connect() must be rejected for an SDK-registered custom provider")
			}
			return
		}
	}
	t.Fatal("provider not found")
}

// TestRegisterOpenAI_FetchModelsHookIsUsed is the direct regression test
// for the SDK's ProviderWithFetchModels override: when a fetchModels
// function is supplied, FetchModels() must call it instead of hitting
// <url>/models over HTTP.
func TestRegisterOpenAI_FetchModelsHookIsUsed(t *testing.T) {
	withCleanRegistry(t)

	// A server that would fail the test if actually hit — proves the hook,
	// not the default HTTP path, produced the results.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("default HTTP models endpoint must not be hit when a fetchModels hook is supplied")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	hookCalled := false
	hook := func() ([]types.ModelMeta, error) {
		hookCalled = true
		return []types.ModelMeta{{ID: "static-a"}, {ID: "static-b"}}, nil
	}

	if err := RegisterOpenAI("hook-proxy", srv.URL, "", nil, hook); err != nil {
		t.Fatalf("RegisterOpenAI: %v", err)
	}

	var found Provider
	for _, p := range All {
		if p.Name() == "hook-proxy" {
			found = p
			break
		}
	}
	if found == nil {
		t.Fatal("hook-proxy not found")
	}

	metas, err := found.FetchModels()
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if !hookCalled {
		t.Error("the ProviderWithFetchModels hook was never called")
	}
	if len(metas) != 2 {
		t.Fatalf("got %d models, want 2 (from the hook, not the HTTP default)", len(metas))
	}
}

func TestRegisterOpenAI_NilFetchModelsUsesDefaultHTTPPath(t *testing.T) {
	withCleanRegistry(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"from-http"}]}`))
	}))
	defer srv.Close()

	if err := RegisterOpenAI("default-fetch-proxy", srv.URL, "", nil, nil); err != nil {
		t.Fatalf("RegisterOpenAI: %v", err)
	}
	var found Provider
	for _, p := range All {
		if p.Name() == "default-fetch-proxy" {
			found = p
			break
		}
	}
	if found == nil {
		t.Fatal("default-fetch-proxy not found")
	}
	metas, err := found.FetchModels()
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != "from-http" {
		t.Errorf("unexpected metas: %+v", metas)
	}
}

// TestRegisterOpenAI_ResolveWorksEndToEnd confirms the registered provider
// is genuinely usable through Resolve("name/model"), the same path
// agent.NewSession drives — not just present in All.
func TestRegisterOpenAI_ResolveWorksEndToEnd(t *testing.T) {
	withCleanRegistry(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"resolve-model"}]}`))
	}))
	defer srv.Close()

	if err := RegisterOpenAI("resolve-proxy", srv.URL, "", nil, nil); err != nil {
		t.Fatalf("RegisterOpenAI: %v", err)
	}

	p, modelID, err := Resolve("resolve-proxy/resolve-model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Name() != "resolve-proxy" || modelID != "resolve-model" {
		t.Errorf("Resolve returned (%s, %s), want (resolve-proxy, resolve-model)", p.Name(), modelID)
	}
}
