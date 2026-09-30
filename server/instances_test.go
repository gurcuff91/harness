package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPurgeDeadInstancesRemovesOnlyUnresponsiveEntries reproduces the real
// incident this exists to fix: a colleague whose process was suspended
// (SIGTSTP/Ctrl+Z, not crashed) never calls UnregisterInstance, so its
// entry sits in instances.json forever — purgeDeadInstances is the one
// place that reclaims it. A genuinely live instance's entry must survive
// untouched.
func TestPurgeDeadInstancesRemovesOnlyUnresponsiveEntries(t *testing.T) {
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"harness"}`))
	}))
	defer alive.Close()

	instances := map[string]InstanceInfo{
		"alive-one":        {URL: alive.URL},
		"dead-unreachable": {URL: "http://127.0.0.1:1"}, // reserved/unassigned — connection refused
		"dead-empty-url":   {URL: ""},
	}

	removed := purgeDeadInstances(instances)

	if removed != 2 {
		t.Errorf("removed = %d, want 2 (the two dead entries)", removed)
	}
	if _, ok := instances["alive-one"]; !ok {
		t.Error("alive-one was removed, want it to survive")
	}
	if _, ok := instances["dead-unreachable"]; ok {
		t.Error("dead-unreachable survived, want it purged")
	}
	if _, ok := instances["dead-empty-url"]; ok {
		t.Error("dead-empty-url survived, want it purged")
	}
	if len(instances) != 1 {
		t.Errorf("len(instances) = %d, want 1", len(instances))
	}
}

// TestPurgeDeadInstancesEmptyMapIsSafe guards against a nil/empty map
// panicking (RegisterInstance can legitimately run against a
// freshly-created, still-empty registry).
func TestPurgeDeadInstancesEmptyMapIsSafe(t *testing.T) {
	if removed := purgeDeadInstances(map[string]InstanceInfo{}); removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if removed := purgeDeadInstances(nil); removed != 0 {
		t.Errorf("removed (nil map) = %d, want 0", removed)
	}
}

// TestRegisterInstancePurgesDeadEntriesFromTheWholeRegistry is the
// end-to-end regression test: RegisterInstance must clean up EVERY dead
// entry already in instances.json, not just one colliding with the
// freshly generated name (generateInstanceName's own narrower reclaim).
func TestRegisterInstancePurgesDeadEntriesFromTheWholeRegistry(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from the real ~/.harness/instances.json

	// Seed the registry directly with one dead entry that would NEVER be
	// touched by generateInstanceName's collision-only reclaim (its
	// randomly generated name is extremely unlikely to collide with
	// "long-dead-ghost").
	seeded := map[string]InstanceInfo{"long-dead-ghost": {URL: "http://127.0.0.1:1"}}
	if err := writeInstances(seeded); err != nil {
		t.Fatalf("seed writeInstances: %v", err)
	}

	name, err := RegisterInstance(InstanceInfo{URL: "http://127.0.0.1:9", PID: 12345})
	if err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}

	instances, err := loadInstances()
	if err != nil {
		t.Fatalf("loadInstances: %v", err)
	}
	if _, ok := instances["long-dead-ghost"]; ok {
		t.Error("long-dead-ghost survived RegisterInstance, want it purged by the whole-registry sweep")
	}
	if _, ok := instances[name]; !ok {
		t.Errorf("newly registered instance %q not found in registry", name)
	}
}
