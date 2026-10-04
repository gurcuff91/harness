package configstore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// store is the method set shared by both ports — the contract suite below
// runs against anything satisfying it, so every implementation (and any
// future one) is held to the exact same behavior.
type store interface {
	Get(namespace, key string) ([]byte, bool, error)
	Set(namespace, key string, value []byte) error
	Delete(namespace, key string) error
	List(namespace string) (map[string][]byte, error)
	SwapValue(namespace, key string, fn func(current []byte, found bool) (next []byte, write bool, err error)) error
	Close() error
}

// implementations returns a fresh, empty instance of every store under test.
func implementations(t *testing.T) map[string]store {
	t.Helper()
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"), 0600)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return map[string]store{
		"FileStore":     fs,
		"InMemoryStore": NewInMemoryStore(),
	}
}

func forEach(t *testing.T, fn func(t *testing.T, s store)) {
	for name, s := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			defer s.Close()
			fn(t, s)
		})
	}
}

func TestContract_GetMissingIsNotFoundNotError(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		v, found, err := s.Get("ns", "nope")
		if err != nil || found || v != nil {
			t.Fatalf("Get(missing) = %q, %v, %v; want nil, false, nil", v, found, err)
		}
	})
}

func TestContract_SetGetUpsert(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		if err := s.Set("ns", "k", []byte(`"one"`)); err != nil {
			t.Fatal(err)
		}
		if err := s.Set("ns", "k", []byte(`"two"`)); err != nil {
			t.Fatal(err)
		}
		v, found, err := s.Get("ns", "k")
		if err != nil || !found || string(v) != `"two"` {
			t.Fatalf("Get after upsert = %q, %v, %v; want \"two\"", v, found, err)
		}
	})
}

func TestContract_NamespacesAreIsolated(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		_ = s.Set("a", "k", []byte(`1`))
		_ = s.Set("b", "k", []byte(`2`))
		if v, _, _ := s.Get("a", "k"); string(v) != `1` {
			t.Errorf("a/k = %q, want 1", v)
		}
		if v, _, _ := s.Get("b", "k"); string(v) != `2` {
			t.Errorf("b/k = %q, want 2", v)
		}
	})
}

func TestContract_DeleteIsIdempotent(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		_ = s.Set("ns", "k", []byte(`true`))
		if err := s.Delete("ns", "k"); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := s.Get("ns", "k"); found {
			t.Error("value still present after Delete")
		}
		if err := s.Delete("ns", "k"); err != nil {
			t.Errorf("deleting a missing entry must not error, got %v", err)
		}
	})
}

func TestContract_List(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		empty, err := s.List("ns")
		if err != nil || empty == nil || len(empty) != 0 {
			t.Fatalf("List(empty) = %v, %v; want empty non-nil map", empty, err)
		}
		_ = s.Set("ns", "a", []byte(`1`))
		_ = s.Set("ns", "b", []byte(`2`))
		_ = s.Set("other", "c", []byte(`3`))
		got, err := s.List("ns")
		if err != nil || len(got) != 2 || string(got["a"]) != `1` || string(got["b"]) != `2` {
			t.Fatalf("List(ns) = %v, %v; want exactly a=1, b=2", got, err)
		}
	})
}

func TestContract_ReturnedBytesAreCopies(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		in := []byte(`"orig"`)
		_ = s.Set("ns", "k", in)
		in[1] = 'X' // mutating the caller's slice must not reach the store
		v, _, _ := s.Get("ns", "k")
		v[1] = 'Y' // mutating a returned slice must not reach the store either
		again, _, _ := s.Get("ns", "k")
		if string(again) != `"orig"` {
			t.Errorf("stored value = %s, want \"orig\" — store aliases caller memory", again)
		}
	})
}

func TestContract_RejectsInvalidJSON(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		if err := s.Set("ns", "k", []byte(`not json`)); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("Set(invalid) = %v, want ErrInvalidValue", err)
		}
		err := s.SwapValue("ns", "k", func([]byte, bool) ([]byte, bool, error) {
			return []byte(`{broken`), true, nil
		})
		if !errors.Is(err, ErrInvalidValue) {
			t.Errorf("SwapValue(invalid) = %v, want ErrInvalidValue", err)
		}
		if _, found, _ := s.Get("ns", "k"); found {
			t.Error("an invalid value must not be stored")
		}
	})
}

func TestContract_SwapValueSeesCurrentAndWrites(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		var sawFound bool
		err := s.SwapValue("ns", "k", func(cur []byte, found bool) ([]byte, bool, error) {
			sawFound = found
			return []byte(`1`), true, nil
		})
		if err != nil || sawFound {
			t.Fatalf("first SwapValue: err=%v found=%v; want nil, false", err, sawFound)
		}
		var saw string
		_ = s.SwapValue("ns", "k", func(cur []byte, found bool) ([]byte, bool, error) {
			saw = string(cur)
			return []byte(`2`), true, nil
		})
		if saw != `1` {
			t.Errorf("fn saw %q, want the current value 1", saw)
		}
		if v, _, _ := s.Get("ns", "k"); string(v) != `2` {
			t.Errorf("value after swap = %s, want 2", v)
		}
	})
}

func TestContract_SwapValueWriteFalseAndErrorDoNotWrite(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		_ = s.Set("ns", "k", []byte(`"keep"`))

		if err := s.SwapValue("ns", "k", func([]byte, bool) ([]byte, bool, error) {
			return []byte(`"no"`), false, nil
		}); err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("boom")
		if err := s.SwapValue("ns", "k", func([]byte, bool) ([]byte, bool, error) {
			return []byte(`"no"`), true, sentinel
		}); !errors.Is(err, sentinel) {
			t.Fatalf("SwapValue error = %v, want the callback's own error", err)
		}
		if v, _, _ := s.Get("ns", "k"); string(v) != `"keep"` {
			t.Errorf("value = %s, want \"keep\" — write=false/error must not write", v)
		}
	})
}

func TestContract_SwapValueIsAtomic(t *testing.T) {
	forEach(t, func(t *testing.T, s store) {
		_ = s.Set("ns", "counter", []byte(`0`))
		const n = 40
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.SwapValue("ns", "counter", func(cur []byte, _ bool) ([]byte, bool, error) {
					return incr(cur), true, nil
				})
				if err != nil {
					t.Errorf("SwapValue: %v", err)
				}
			}()
		}
		wg.Wait()
		if v, _, _ := s.Get("ns", "counter"); string(v) != "40" {
			t.Errorf("counter = %s, want 40 — concurrent SwapValue lost updates", v)
		}
	})
}

// incr parses a small non-negative JSON integer and returns it plus one.
func incr(b []byte) []byte {
	n := 0
	for _, c := range b {
		n = n*10 + int(c-'0')
	}
	n++
	if n == 0 {
		return []byte("0")
	}
	var out []byte
	for ; n > 0; n /= 10 {
		out = append([]byte{byte('0' + n%10)}, out...)
	}
	return out
}
