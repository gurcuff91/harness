package providers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// flakyCreds is a credentials store whose reads fail on demand (a remote
// store that did not answer).
type flakyCreds struct {
	configstore.CredentialsStore
	err error
}

func (f *flakyCreds) Get(ns, k string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	return f.CredentialsStore.Get(ns, k)
}

func flakyStore(t *testing.T) *flakyCreds {
	t.Helper()
	st := &flakyCreds{CredentialsStore: configstore.NewInMemoryStore()}
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), st))
	return st
}

func TestResolveAPIKeySurfacesStoreFailure(t *testing.T) {
	st := flakyStore(t)
	t.Setenv("HX_TEST_KEY", "")
	if err := storeAPIKey("p", "k1"); err != nil {
		t.Fatal(err)
	}
	if k, src, err := resolveAPIKeyErr("p", "HX_TEST_KEY"); err != nil || k != "k1" || src != ActivationCredentials {
		t.Fatalf("healthy = %q %v %v", k, src, err)
	}
	if _, src, err := resolveAPIKeyErr("missing", "HX_TEST_KEY"); err != nil || src != ActivationNone {
		t.Fatalf("not found = %v %v; want ActivationNone, nil", src, err)
	}

	boom := errors.New("transport down")
	st.err = boom
	if k, src, err := resolveAPIKeyErr("p", "HX_TEST_KEY"); !errors.Is(err, boom) || k != "" || src != ActivationNone {
		t.Fatalf("failing = %q %v %v; want the store's error", k, src, err)
	}
	// An env var still wins and needs no store.
	t.Setenv("HX_TEST_KEY", "from-env")
	if k, src, err := resolveAPIKeyErr("p", "HX_TEST_KEY"); err != nil || k != "from-env" || src != ActivationEnvVar {
		t.Fatalf("env = %q %v %v", k, src, err)
	}
}

func TestAPIKeyProviderReportsReadFailureAndKeepsLastKnownState(t *testing.T) {
	st := flakyStore(t)
	t.Setenv(anthropicAPIKeyEnv, "")
	if err := storeAPIKey("anthropic", "k1"); err != nil {
		t.Fatal(err)
	}
	a := &Anthropic{}
	if _, err := a.ResolveCredentials(); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("transport down")
	st.err = boom
	// Already resolved: stays connected through the outage.
	if got := a.ActivationSource(); got != ActivationCredentials {
		t.Errorf("ActivationSource during an outage = %v, want the last known (credentials)", got)
	}
	// Never resolved: the error is the read failure, not "no credentials found".
	fresh := &Anthropic{}
	_, err := fresh.ResolveCredentials()
	if !errors.Is(err, boom) || strings.Contains(err.Error(), "no credentials found") {
		t.Errorf("ResolveCredentials during an outage = %v, want the store's error", err)
	}
	if fresh.IsActive() {
		t.Error("a provider that never resolved a key cannot be active during an outage")
	}
}

func TestOAuthLoadSurfacesReadFailureOrKeepsMemory(t *testing.T) {
	st := flakyStore(t)
	cm := config.GetCredentialsManager()
	cred := config.ProviderCredential{Type: "oauth", AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := cm.SetCredential("claude-oauth", cred); err != nil {
		t.Fatal(err)
	}
	c, err0 := NewClaudeOAuth()
	if err0 != nil {
		t.Fatal(err0)
	}
	if _, err := c.ResolveCredentials(); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("transport down")
	st.err = boom
	// Memory holds the last known credentials: still usable, still active.
	if _, err := c.ResolveCredentials(); err != nil {
		t.Errorf("ResolveCredentials with credentials in memory during an outage: %v", err)
	}
	// Nothing in memory: report the read failure, not "no credentials found".
	st.err = nil
	cm.DeleteCredential("claude-oauth")
	st.err = boom
	empty := &ClaudeOAuth{tokens: newTokenManager()}
	err := empty.loadCredentialsFromSources()
	if !errors.Is(err, boom) {
		t.Errorf("load with nothing in memory during an outage = %v, want the store's error", err)
	}
	// And a plain not-found stays "no credentials found".
	st.err = nil
	err = empty.loadCredentialsFromSources()
	if err == nil || !strings.Contains(err.Error(), "no credentials found") {
		t.Errorf("not found = %v, want no credentials found", err)
	}
}
