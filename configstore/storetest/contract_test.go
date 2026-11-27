package storetest

import (
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/configstore"
)

// Both ports must satisfy the suite's Store.
var (
	_ Store = configstore.SettingsStore(nil)
	_ Store = configstore.CredentialsStore(nil)
)

// recorder is a TB that records failures; Fatal stops the case like testing.T.
type recorder struct {
	mu     sync.Mutex
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) fail()                 { r.mu.Lock(); r.failed = true; r.mu.Unlock() }
func (r *recorder) Error(...any)          { r.fail() }
func (r *recorder) Errorf(string, ...any) { r.fail() }
func (r *recorder) Fatal(...any)          { r.fail(); runtime.Goexit() }
func (r *recorder) Fatalf(string, ...any) { r.fail(); runtime.Goexit() }

// runCase drives one case against s on its own goroutine, reporting failure.
func runCase(c Case, s Store) bool {
	r := &recorder{}
	done := make(chan struct{})
	go func() { defer close(done); c.Run(r, s) }()
	<-done
	return r.failed
}

func caseNamed(t *testing.T, name string) Case {
	for _, c := range Cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no case %q", name)
	return Case{}
}

func TestSuitePassesOnInMemoryStore(t *testing.T) {
	RunContract(t, func(*testing.T) Store { return configstore.NewInMemoryStore() })
}

// aliasing returns its internal slice from Get; racy swaps lose updates;
// the others break one rule each.
type broken struct {
	Store
	mode string
}

func (b broken) Get(ns, k string) ([]byte, bool, error) {
	v, ok, err := b.Store.Get(ns, k)
	if b.mode == "alias" && ok {
		return v, ok, err
	}
	if b.mode == "missing-error" && !ok {
		return nil, false, fmt.Errorf("not found")
	}
	return v, ok, err
}

// aliasStore keeps the caller's and the returned slices as-is.
type aliasStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (a *aliasStore) Get(ns, k string) ([]byte, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.m[ns+"/"+k]
	return v, ok, nil
}
func (a *aliasStore) Set(ns, k string, v []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[ns+"/"+k] = v
	return nil
}
func (a *aliasStore) Delete(string, string) error            { return nil }
func (a *aliasStore) List(string) (map[string][]byte, error) { return map[string][]byte{}, nil }
func (a *aliasStore) Close() error                           { return nil }
func (a *aliasStore) SwapValue(ns, k string, fn func([]byte, bool) ([]byte, bool, error)) error {
	return nil
}

// racySwap reads, runs fn and writes without holding anything across them.
type racySwap struct{ Store }

func (r racySwap) SwapValue(ns, k string, fn func([]byte, bool) ([]byte, bool, error)) error {
	cur, found, _ := r.Store.Get(ns, k)
	next, write, err := fn(cur, found)
	if err != nil || !write {
		return err
	}
	runtime.Gosched()
	return r.Store.Set(ns, k, next)
}

func TestSuiteCatchesBrokenStores(t *testing.T) {
	good := func() Store { return configstore.NewInMemoryStore() }
	tests := []struct {
		name, caseName string
		store          Store
	}{
		{"aliased bytes", "ReturnedBytesAreCopies", &aliasStore{m: map[string][]byte{}}},
		{"missing key is an error", "GetMissingIsNotFoundNotError", broken{good(), "missing-error"}},
		{"non-atomic swap", "SwapValueIsAtomic", racySwap{good()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !runCase(caseNamed(t, tc.caseName), tc.store) {
				t.Fatalf("case %s passed on a broken store (%s)", tc.caseName, tc.name)
			}
		})
	}
	// Sanity: the recorder reports a pass for a healthy store.
	if runCase(caseNamed(t, "SwapValueIsAtomic"), good()) {
		t.Fatal("healthy store failed")
	}
}
