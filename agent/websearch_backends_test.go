package agent

import (
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
)

// synthetic is the third search backend: present only with a key, last in
// the order, aimed at the documented endpoint.
func TestWebSearchBackendsIncludeSyntheticOnlyWithAKey(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	for _, k := range []string{"MINIMAX_API_KEY", "OLLAMA_API_KEY", "SYNTHETIC_API_KEY"} {
		t.Setenv(k, "")
	}
	// A fresh provider: the registry's own instance memoizes a resolved key.
	providers.EnsureRegistry()
	list := []providers.Provider{}
	for _, p := range providers.All() {
		if p.Name() == "synthetic" {
			p = providers.NewSynthetic()
		}
		list = append(list, p)
	}
	t.Cleanup(providers.SetForTest(list))
	a := &Agent{}
	for _, b := range a.webSearchBackends() {
		if b.Name == "synthetic" {
			t.Fatal("synthetic active without a key")
		}
	}
	t.Setenv("SYNTHETIC_API_KEY", "k-syn")
	var got *struct{ url, key string }
	names := []string{}
	for _, b := range a.webSearchBackends() {
		names = append(names, b.Name)
		if b.Name == "synthetic" {
			got = &struct{ url, key string }{b.URL, b.Key}
		}
	}
	if got == nil || got.url != "https://api.synthetic.new/v2/search" || got.key != "k-syn" {
		t.Fatalf("backends = %v, synthetic = %+v", names, got)
	}
	if names[len(names)-1] != "synthetic" {
		t.Errorf("synthetic must come last, got %v", names)
	}
}
