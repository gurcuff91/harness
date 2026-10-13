package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// Models() fetches each provider's uncached model list CONCURRENTLY: three
// providers that each take 300ms cost ~300ms total, not ~900ms — and the
// listing keeps the providers' registration order.
func TestModelsFetchesProvidersConcurrently(t *testing.T) {
	withCleanProviderRegistry(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	names := []string{"par-a", "par-b", "par-c"}
	for _, n := range names {
		id := n + "-model"
		if err := NewOpenAIProvider(n, ts.URL, ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
			time.Sleep(300 * time.Millisecond)
			return []types.ModelMeta{{ID: id}}, nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	start := time.Now()
	list := a.Models()
	el := time.Since(start)

	var ours []string
	for _, m := range list {
		for _, n := range names {
			if m.Provider == n {
				ours = append(ours, m.Model)
			}
		}
	}
	want := []string{"par-a/par-a-model", "par-b/par-b-model", "par-c/par-c-model"}
	if len(ours) != 3 || ours[0] != want[0] || ours[1] != want[1] || ours[2] != want[2] {
		t.Fatalf("listing = %v, want %v in order", ours, want)
	}
	if el > 750*time.Millisecond {
		t.Errorf("Models() took %v — providers must be fetched concurrently (~300ms, not ~900ms)", el)
	}
}
