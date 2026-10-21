package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
)

// withSettings isolates the settings store (an in-memory one) and the
// provider registry for a reload test.
func withSettings(t *testing.T) *config.SettingsManager {
	t.Helper()
	withCleanProviderRegistry(t)
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
	return config.GetSettingsManager()
}

func setProvider(t *testing.T, sm *config.SettingsManager, name, url, key string, disabled bool) {
	t.Helper()
	if err := sm.SetCustomProvider(name, config.CustomProvider{Type: "openai", URL: url, Headers: map[string]string{"X-Api-Key": key}, Disabled: disabled}); err != nil {
		t.Fatal(err)
	}
}

// Criterion 10a: an added entry appears; a deleted or disabled one disappears.
func TestReloadAddsAndRemoves(t *testing.T) {
	sm := withSettings(t)
	g := newGateway(t)

	setProvider(t, sm, "set-a", g.URL, "k", false)
	setProvider(t, sm, "set-b", g.URL, "k", false)
	if err := ReloadCustomProviders(); err != nil {
		t.Fatal(err)
	}
	if !hasProvider("set-a") || !hasProvider("set-b") {
		t.Fatal("added entries must appear after a reload")
	}

	sm.DeleteCustomProvider("set-a")
	setProvider(t, sm, "set-b", g.URL, "k", true) // disabled
	if err := ReloadCustomProviders(); err != nil {
		t.Fatal(err)
	}
	if hasProvider("set-a") || hasProvider("set-b") {
		t.Fatal("deleted and disabled entries must disappear after a reload")
	}
}

// Criteria 10b + 5 + 6: an edited entry (new URL/headers) is used by the next
// turn of a session already on it; the model list reflects the new provider.
func TestReloadEditIsPickedUpByRunningSession(t *testing.T) {
	sm := withSettings(t)
	g := newGateway(t)
	setProvider(t, sm, "set-edit", g.URL, "old", false)
	if err := ReloadCustomProviders(); err != nil {
		t.Fatal(err)
	}
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()
	sess, err := a.NewSession(t.TempDir(), "set-edit/m")
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := sess.PromptAndWait(context.Background(), "one"); out != "key=old" {
		t.Fatalf("first turn = %q", out)
	}

	setProvider(t, sm, "set-edit", g.URL, "new", false)
	if err := ReloadCustomProviders(); err != nil {
		t.Fatal(err)
	}
	if out, err := sess.PromptAndWait(context.Background(), "two"); err != nil || out != "key=new" {
		t.Fatalf("turn after edit = %q, %v; want the NEW headers", out, err)
	}
	if !modelListed(a, "set-edit/m") {
		t.Error("the reloaded provider's models must be listed")
	}

	sm.DeleteCustomProvider("set-edit")
	ReloadCustomProviders()
	if _, err := sess.PromptAndWait(context.Background(), "three"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("turn after delete = %v, want ErrProviderNotFound", err)
	}
	if modelListed(a, "set-edit/m") {
		t.Error("a deleted provider's models must leave the model list")
	}
}

// Criteria 2 + 10c: built-in and programmatic providers are untouched — the
// very same objects survive a reload.
func TestReloadLeavesBuiltinAndProgrammatic(t *testing.T) {
	sm := withSettings(t)
	g := newGateway(t)
	if err := NewOpenAIProvider("code-p", g.URL); err != nil {
		t.Fatal(err)
	}
	before := map[string]providers.Provider{}
	for _, p := range providers.All() {
		if providers.IsSettingsDeclared(p) {
			continue // settings providers ARE reload's business (the real settings.json may have some)
		}
		before[p.Name()] = p
	}
	setProvider(t, sm, "set-x", g.URL, "k", false)
	if err := ReloadCustomProviders(); err != nil {
		t.Fatal(err)
	}
	for name, p := range before {
		if providers.Find(name) != p {
			t.Errorf("%s was replaced or removed by a reload", name)
		}
	}
}

// Criterion 3: a settings entry named like a programmatic provider is skipped
// (the programmatic one wins), the rest still applies, and the error names it.
func TestReloadNameConflictProgrammaticWins(t *testing.T) {
	sm := withSettings(t)
	g := newGateway(t)
	if err := NewOpenAIProvider("dup", g.URL, ProviderWithHeaders(map[string]string{"X-Api-Key": "code"})); err != nil {
		t.Fatal(err)
	}
	code := providers.Find("dup")
	setProvider(t, sm, "dup", g.URL, "settings", false)
	setProvider(t, sm, "set-ok", g.URL, "k", false)

	err := ReloadCustomProviders()
	if !errors.Is(err, ErrProviderNameConflict) || !strings.Contains(err.Error(), "dup") {
		t.Fatalf("err = %v, want ErrProviderNameConflict naming dup", err)
	}
	if providers.Find("dup") != code {
		t.Error("the programmatic provider must win the name")
	}
	if !hasProvider("set-ok") {
		t.Error("the rest of the reload must still be applied")
	}
	n := 0
	for _, p := range providers.All() {
		if p.Name() == "dup" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d providers named dup, want exactly 1", n)
	}
}

// Criterion 8: idempotent — reloading without changes keeps the same
// provider objects (and so their model caches).
func TestReloadIsIdempotent(t *testing.T) {
	sm := withSettings(t)
	g := newGateway(t)
	setProvider(t, sm, "set-same", g.URL, "k", false)
	ReloadCustomProviders()
	first := providers.Find("set-same")
	first.FetchModels() // warm its cache
	if err := ReloadCustomProviders(); err != nil {
		t.Fatal(err)
	}
	if providers.Find("set-same") != first {
		t.Fatal("an unchanged entry must keep its provider object across reloads")
	}
	if len(first.Models()) == 0 {
		t.Error("the model cache must survive an unchanged reload")
	}
}

// Criterion 4: a snapshot handed out before a reload is untouched by it.
func TestReloadKeepsOldSnapshots(t *testing.T) {
	sm := withSettings(t)
	g := newGateway(t)
	setProvider(t, sm, "set-snap", g.URL, "k", false)
	ReloadCustomProviders()
	snap := providers.All()
	n := len(snap)
	sm.DeleteCustomProvider("set-snap")
	ReloadCustomProviders()
	if len(snap) != n {
		t.Fatal("an old snapshot changed length")
	}
	found := false
	for _, p := range snap {
		found = found || p.Name() == "set-snap"
	}
	if !found {
		t.Error("the old snapshot must still hold the provider it had")
	}
}

// Criterion 7: reloads racing with registry readers and live listing.
func TestReloadRacesWithReaders(t *testing.T) {
	sm := withSettings(t)
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
				providers.Find("set-race")
				providers.Resolve("set-race/m")
				for _, p := range providers.All() {
					_ = p.Name()
				}
			}
		}()
	}
	for i := range 200 {
		setProvider(t, sm, "set-race", g.URL, fmt.Sprint(i), i%3 == 0)
		if err := ReloadCustomProviders(); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
	}
	stop.Store(true)
	wg.Wait()
}
