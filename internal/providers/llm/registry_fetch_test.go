package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

// catalog is a valid OpenRouter-shaped body with one priced model.
func catalog(price string) string {
	return fmt.Sprintf(`{"data":[{"id":"anthropic/claude-test-9","name":"Claude Test","context_length":200000,
"pricing":{"prompt":"%s","completion":"0.000015"},"top_provider":{"max_completion_tokens":64000}}]}`, price)
}

// fakeRegistry serves the catalog per a scripted list of behaviors, one per
// request ("ok", "stall", "5xx", "malformed"); the last one repeats.
type fakeRegistry struct {
	mu      sync.Mutex
	script  []string
	price   string
	queries []string
	calls   atomic.Int64
	release chan struct{} // closes stalled handlers at cleanup
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(f.calls.Add(1)) - 1
	f.mu.Lock()
	f.queries = append(f.queries, r.URL.RawQuery)
	mode := f.script[min(n, len(f.script)-1)]
	price := f.price
	f.mu.Unlock()
	switch mode {
	case "ok":
		w.Write([]byte(catalog(price)))
	case "5xx":
		w.WriteHeader(http.StatusBadGateway)
	case "malformed":
		w.Write([]byte(`{"data":[{"id":"x",`)) // truncated JSON
	case "stall": // headers + part of the body, then hang (the observed CDN bug)
		w.Header().Set("Content-Length", "700000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-f.release:
		}
	}
}

// withFakeRegistry points the registry at f with fast timings and an isolated
// disk cache, restoring everything afterwards.
func withFakeRegistry(t *testing.T, f *fakeRegistry) (cachePath string) {
	t.Helper()
	f.release = make(chan struct{})
	ts := httptest.NewServer(f)
	cachePath = filepath.Join(t.TempDir(), "cache", "model-registry.json")
	oldURL, oldTimeout, oldDelays, oldBackoff, oldPath := registryURL, registryHTTPTimeout, registryRetryDelays, registryRefreshBackoff, registryCachePath
	registryURL = ts.URL + "/api/v1/models"
	registryHTTPTimeout = 300 * time.Millisecond
	registryRetryDelays = []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	registryRefreshBackoff = []time.Duration{50 * time.Millisecond}
	registryCachePath = func() string { return cachePath }
	resetRegistryForTest()
	t.Cleanup(func() {
		close(f.release)
		ts.Close()
		registryURL, registryHTTPTimeout, registryRetryDelays, registryRefreshBackoff, registryCachePath = oldURL, oldTimeout, oldDelays, oldBackoff, oldPath
		resetRegistryForTest()
	})
	return cachePath
}

func inputPrice(t *testing.T) float64 {
	t.Helper()
	m := types.ModelMeta{ID: "claude-test-9"}
	ApplyRegistryPricing(&m)
	return m.InputPrice
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Every request carries a fresh cache-buster.
func TestRegistryCacheBuster(t *testing.T) {
	f := &fakeRegistry{script: []string{"5xx", "5xx", "ok"}, price: "0.000003"}
	withFakeRegistry(t, f)
	if p := inputPrice(t); p != 3 {
		t.Fatalf("price = %v, want 3", p)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for _, q := range f.queries {
		if !strings.HasPrefix(q, "ts=") || seen[q] {
			t.Errorf("query %q: want a fresh ts= cache-buster on every request", q)
		}
		seen[q] = true
	}
}

// A body that stalls after the headers is cut by the timeout and retried; the
// retry succeeds.
func TestRegistryStallThenRetrySucceeds(t *testing.T) {
	f := &fakeRegistry{script: []string{"stall", "ok"}, price: "0.000003"}
	path := withFakeRegistry(t, f)
	start := time.Now()
	if p := inputPrice(t); p != 3 {
		t.Fatalf("price = %v, want 3 (from the retry)", p)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %v — the stalled body must be cut by the timeout", el)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("a successful fetch must write the disk cache: %v", err)
	}
}

// Every attempt fails (stall/5xx/malformed) with a disk cache present: served
// from the cache immediately, never hanging; a later success refreshes the
// prices WITHOUT a restart and rewrites the disk cache.
func TestRegistryAllFailFallsBackToDiskThenRefreshes(t *testing.T) {
	for _, mode := range []string{"stall", "5xx", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeRegistry{script: []string{mode}, price: "0.000003"}
			path := withFakeRegistry(t, f)
			saveRegistryCache(currentRegistryConfig(), parseRegistry([]byte(catalog("0.000001")))) // last good copy: $1

			start := time.Now()
			if p := inputPrice(t); p != 1 {
				t.Fatalf("price = %v, want the disk copy's 1", p)
			}
			if el := time.Since(start); el > 100*time.Millisecond {
				t.Errorf("with a disk copy the first call must not wait on the network (took %v)", el)
			}

			v := RegistryVersion()
			updated := make(chan struct{}, 1)
			OnRegistryUpdate(func() { updated <- struct{}{} })
			// Let the background refresher actually hit the failing server
			// first — otherwise it could start after the recovery below and
			// this test would never exercise the failure path.
			eventually(t, "a failed background attempt", func() bool { return f.calls.Load() >= 1 })
			f.mu.Lock()
			f.script = []string{"ok"} // the server recovers
			f.mu.Unlock()
			select {
			case <-updated:
			case <-time.After(5 * time.Second):
				t.Fatal("the background refresher never delivered the recovered catalog")
			}
			if p := inputPrice(t); p != 3 || RegistryVersion() == v {
				t.Fatalf("after recovery price = %v (want 3), version changed = %v", p, RegistryVersion() != v)
			}
			eventually(t, "disk cache refresh", func() bool {
				data, _ := os.ReadFile(path)
				return strings.Contains(string(data), `"InputPrice":3`) || strings.Contains(string(data), `"input_price":3`)
			})
		})
	}
}

// Every attempt fails and there is NO disk cache: returns within the timeout
// + retries (never hangs), with no remote data — the hardcoded registry and
// inference still apply — and keeps refreshing in the background.
func TestRegistryAllFailNoCacheFallsBackToBuiltin(t *testing.T) {
	f := &fakeRegistry{script: []string{"stall"}}
	withFakeRegistry(t, f)

	done := make(chan types.ModelMeta, 1)
	go func() { done <- EnrichMeta(types.ModelMeta{ID: "deepseek-v4"}) }()
	select {
	case m := <-done:
		if m.ContextWindow != 1000000 || !m.Thinking {
			t.Errorf("built-in data not applied: %+v", m)
		}
	case <-time.After(3 * time.Second): // 3 × 300ms timeouts + 60ms backoff
		t.Fatal("EnrichMeta hung on a stalled registry")
	}
	if inputPrice(t) != 0 {
		t.Error("no remote price expected without a catalog")
	}
	before := f.calls.Load()
	eventually(t, "background retries", func() bool { return f.calls.Load() > before })
}

// Concurrent first callers share ONE fetch (no stampede).
func TestRegistryConcurrentFirstCallersShareFetch(t *testing.T) {
	f := &fakeRegistry{script: []string{"ok"}, price: "0.000003"}
	withFakeRegistry(t, f)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); inputPrice(t) }()
	}
	wg.Wait()
	if n := f.calls.Load(); n != 1 {
		t.Errorf("registry fetched %d times, want 1", n)
	}
}
