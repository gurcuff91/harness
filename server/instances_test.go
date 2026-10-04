package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// isolateRegistry points the process-global settings store at a fresh
// in-memory one for the duration of the test — the registry must never touch
// the real ~/.harness while tests run.
func isolateRegistry(t *testing.T) *config.SettingsManager {
	t.Helper()
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
	return config.GetSettingsManager()
}

func aliveServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"harness"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deadURL is reserved/unassigned — connection refused, fast.
const deadURL = "http://127.0.0.1:1"

// The incident this exists for: a suspended (not crashed) colleague never
// unregisters — the purge is the only thing that reclaims it. A live
// instance's entry must survive untouched.
func TestPurgeDeadInstancesRemovesOnlyUnresponsiveEntries(t *testing.T) {
	settings := isolateRegistry(t)
	alive := aliveServer(t)
	settings.ReserveInstance("alive-one", config.InstanceEntry{URL: alive.URL, PID: 1})
	settings.ReserveInstance("dead-unreachable", config.InstanceEntry{URL: deadURL, PID: 2})
	settings.ReserveInstance("dead-empty-url", config.InstanceEntry{PID: 3})

	if removed := purgeDeadInstances(settings); removed != 2 {
		t.Errorf("removed = %d, want 2 (the two dead entries)", removed)
	}
	got := settings.Instances()
	if _, ok := got["alive-one"]; !ok || len(got) != 1 {
		t.Errorf("instances after purge = %v, want only alive-one", got)
	}
}

func TestPurgeDeadInstancesEmptyRegistryIsSafe(t *testing.T) {
	settings := isolateRegistry(t)
	if removed := purgeDeadInstances(settings); removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

// RegisterInstance purges EVERY dead entry, not just one colliding with the
// generated name.
func TestRegisterInstancePurgesDeadEntriesFromTheWholeRegistry(t *testing.T) {
	settings := isolateRegistry(t)
	settings.ReserveInstance("long-dead-ghost", config.InstanceEntry{URL: deadURL, PID: 99})

	name, err := RegisterInstance(InstanceInfo{URL: "http://127.0.0.1:9", PID: 12345})
	if err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	got := settings.Instances()
	if _, ok := got["long-dead-ghost"]; ok {
		t.Error("long-dead-ghost survived RegisterInstance")
	}
	if got[name].PID != 12345 {
		t.Errorf("newly registered instance %q not found: %v", name, got)
	}
}

// The purge must not delete a registration made AFTER the dead one was probed:
// if a new process took the same name meanwhile, its entry carries a
// different PID and is left alone.
func TestPurgeSparesAReRegisteredName(t *testing.T) {
	settings := isolateRegistry(t)
	settings.ReserveInstance("shared-name", config.InstanceEntry{URL: deadURL, PID: 1})

	// Simulate the race: the dead entry is removed and a new process
	// re-registers the same name before the purge acts on its stale probe.
	if err := settings.DeleteInstanceIf("shared-name", 1); err != nil {
		t.Fatal(err)
	}
	if ok, _ := settings.ReserveInstance("shared-name", config.InstanceEntry{URL: deadURL, PID: 2}); !ok {
		t.Fatal("re-registering a vacated name should succeed")
	}
	// The purge's stale decision targets PID 1 — PID 2 must survive.
	if err := settings.DeleteInstanceIf("shared-name", 1); err != nil {
		t.Fatal(err)
	}
	if got := settings.Instances()["shared-name"]; got.PID != 2 {
		t.Errorf("re-registered entry was deleted by a stale purge: %+v", got)
	}
}

// Two "processes" (independent FileStores on one settings file) racing to
// register must always end up with distinct names — never both on one.
func TestConcurrentRegistrationsGetDistinctNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	var managers []*config.SettingsManager
	for range 2 {
		fs, err := configstore.NewFileStore(path, 0600)
		if err != nil {
			t.Fatal(err)
		}
		managers = append(managers, config.NewSettingsManager(fs))
	}
	alive := aliveServer(t)

	// Hammer ONE candidate name from both sides: only one reservation wins.
	var wg sync.WaitGroup
	wins := make(chan int, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := managers[i%2]
			if ok, err := m.ReserveInstance("contested", config.InstanceEntry{URL: alive.URL, PID: 1000 + i}); err == nil && ok {
				wins <- i
			}
		}()
	}
	wg.Wait()
	close(wins)
	if n := len(wins); n != 1 {
		t.Fatalf("%d concurrent reservations of one name succeeded, want exactly 1", n)
	}
}

func TestUnregisterInstanceIsIdempotent(t *testing.T) {
	settings := isolateRegistry(t)
	settings.ReserveInstance("me", config.InstanceEntry{PID: 1})
	UnregisterInstance("me")
	UnregisterInstance("me")
	if _, ok := settings.Instances()["me"]; ok {
		t.Error("instance still registered after UnregisterInstance")
	}
}
