package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// gateway is a fake OpenAI-compatible endpoint that records the X-Api-Key of
// every completion and answers with it, so a test can tell which
// registration (which key) served a turn.
type gateway struct {
	*httptest.Server
	keys []string
	mu   sync.Mutex
}

func newGateway(t *testing.T) *gateway {
	g := &gateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		key := r.Header.Get("X-Api-Key")
		g.mu.Lock()
		g.keys = append(g.keys, key)
		g.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":"key=%s"}}]}`+"\n\n", key)
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(g.Close)
	return g
}

func hasProvider(name string) bool { return providers.Find(name) != nil }

func modelListed(a *Agent, full string) bool {
	for _, m := range a.Models() {
		if m.Model == full {
			return true
		}
	}
	return false
}

// Criteria 1 + 2: register → unregister (gone from the registry, the model
// list and Resolve) → register again with NEW headers, which are used.
func TestUnregisterThenRegisterAgainWithNewHeaders(t *testing.T) {
	withCleanProviderRegistry(t)
	g := newGateway(t)
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	if err := NewOpenAIProvider("tenant-x", g.URL, ProviderWithHeaders(map[string]string{"X-Api-Key": "old"})); err != nil {
		t.Fatal(err)
	}
	if !modelListed(a, "tenant-x/m") {
		t.Fatal("registered provider's model must be listed")
	}

	if err := UnregisterCustomProvider("tenant-x"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if hasProvider("tenant-x") || modelListed(a, "tenant-x/m") {
		t.Fatal("an unregistered provider must leave the registry and the model list")
	}
	if _, _, err := providers.Resolve("tenant-x/m"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("Resolve after unregister = %v, want ErrProviderNotFound", err)
	}

	if err := NewOpenAIProvider("tenant-x", g.URL, ProviderWithHeaders(map[string]string{"X-Api-Key": "new"})); err != nil {
		t.Fatalf("register again: %v", err)
	}
	sess, err := a.NewSession(t.TempDir(), "tenant-x/m")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := sess.PromptAndWait(context.Background(), "hi"); err != nil || out != "key=new" {
		t.Fatalf("turn = %q, %v; want it served with the NEW key", out, err)
	}
}

// Criteria 3 + 5: built-in providers can't be unregistered; unknown names
// report ErrProviderNotFound.
func TestUnregisterRejectsBuiltinAndUnknown(t *testing.T) {
	withCleanProviderRegistry(t)
	for _, name := range []string{"anthropic", "openai", "minimax", "claude-oauth"} {
		if !hasProvider(name) {
			continue // not constructed in this environment
		}
		err := UnregisterCustomProvider(name)
		if !errors.Is(err, ErrProviderNotRemovable) {
			t.Errorf("%s: err = %v, want ErrProviderNotRemovable", name, err)
		}
		if !hasProvider(name) {
			t.Errorf("%s: a built-in provider was removed", name)
		}
	}
	if err := UnregisterCustomProvider("no-such-provider"); !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("unknown: err = %v, want ErrProviderNotFound", err)
	}
}

// Criterion 4: a custom provider declared in settings.json can't be
// unregistered through this API.
func TestUnregisterRejectsSettingsDeclaredProvider(t *testing.T) {
	withCleanProviderRegistry(t)
	declared := providers.NewCustomOpenAI("from-settings", config.CustomProvider{Type: "openai", URL: "http://127.0.0.1:1"})
	providers.AddForTest(declared)

	err := UnregisterCustomProvider("from-settings")
	if !errors.Is(err, ErrProviderNotRemovable) {
		t.Fatalf("err = %v, want ErrProviderNotRemovable", err)
	}
	if !hasProvider("from-settings") {
		t.Fatal("a settings.json provider was removed")
	}
}

// Criterion 7: a RUNNING session whose provider is unregistered fails its
// next turn with a clear not-found error (no panic) — and, once the name is
// registered again, the SAME session adopts the new registration (new key)
// on its following turn.
func TestRunningSessionAfterUnregister(t *testing.T) {
	withCleanProviderRegistry(t)
	g := newGateway(t)
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()
	NewOpenAIProvider("tenant-y", g.URL, ProviderWithHeaders(map[string]string{"X-Api-Key": "k1"}))
	sess, err := a.NewSession(t.TempDir(), "tenant-y/m")
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := sess.PromptAndWait(context.Background(), "one"); out != "key=k1" {
		t.Fatalf("first turn = %q", out)
	}

	UnregisterCustomProvider("tenant-y")
	var events []types.Event
	var mu sync.Mutex
	sess.Subscribe(func(e types.Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	_, err = sess.PromptAndWait(context.Background(), "two")
	if !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("turn after unregister = %v, want ErrProviderNotFound", err)
	}
	mu.Lock()
	sawErr := countType(events, types.EventError) > 0
	mu.Unlock()
	if !sawErr {
		t.Error("the failed turn must emit an error event")
	}

	NewOpenAIProvider("tenant-y", g.URL, ProviderWithHeaders(map[string]string{"X-Api-Key": "k2"}))
	if out, err := sess.PromptAndWait(context.Background(), "three"); err != nil || out != "key=k2" {
		t.Fatalf("turn after re-register = %q, %v; want the same session on the NEW key", out, err)
	}
}

// Criterion 6: unregister/register racing with concurrent registry readers —
// Models(), Resolve, status listing — is race-free (run with -race).
func TestUnregisterRacesWithReaders(t *testing.T) {
	withCleanProviderRegistry(t)
	g := newGateway(t)
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				a.Models()
				providers.Resolve("race-p/m")
				providers.GetProviderStatuses()
				for _, p := range providers.All() {
					_ = p.Name()
				}
			}
		}()
	}
	for i := range 200 {
		if err := NewOpenAIProvider("race-p", g.URL, ProviderWithHeaders(map[string]string{"X-Api-Key": fmt.Sprint(i)})); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
		if err := UnregisterCustomProvider("race-p"); err != nil {
			t.Fatalf("unregister %d: %v", i, err)
		}
	}
	stop.Store(true)
	wg.Wait()
}
