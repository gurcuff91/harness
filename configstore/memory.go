package configstore

import (
	"encoding/json"
	"sync"
)

// InMemoryStore is a process-local implementation of both SettingsStore and
// CredentialsStore — nothing touches disk and everything is lost when the
// process exits. Useful for tests and for SDK embedders that want harness to
// leave no files behind. Atomicity holds within one process only (there is
// no other process that could see this data).
type InMemoryStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

// Compile-time proof InMemoryStore satisfies both ports.
var (
	_ SettingsStore    = (*InMemoryStore)(nil)
	_ CredentialsStore = (*InMemoryStore)(nil)
)

// NewInMemoryStore returns an empty in-memory store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{data: map[string]map[string][]byte{}}
}

func (s *InMemoryStore) Get(namespace, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[namespace][key]
	if !ok {
		return nil, false, nil
	}
	return cloneBytes(v), true, nil
}

func (s *InMemoryStore) Set(namespace, key string, value []byte) error {
	if !json.Valid(value) {
		return ErrInvalidValue
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(namespace, key, value)
	return nil
}

func (s *InMemoryStore) Delete(namespace, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[namespace], key)
	if len(s.data[namespace]) == 0 {
		delete(s.data, namespace)
	}
	return nil
}

func (s *InMemoryStore) List(namespace string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.data[namespace]))
	for k, v := range s.data[namespace] {
		out[k] = cloneBytes(v)
	}
	return out, nil
}

func (s *InMemoryStore) SwapValue(namespace, key string, fn func(current []byte, found bool) (next []byte, write bool, err error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, found := s.data[namespace][key]
	next, write, err := fn(cloneBytes(cur), found)
	if err != nil || !write {
		return err
	}
	if !json.Valid(next) {
		return ErrInvalidValue
	}
	s.put(namespace, key, next)
	return nil
}

func (s *InMemoryStore) Close() error { return nil }

// put stores value (already validated). Caller holds s.mu.
func (s *InMemoryStore) put(namespace, key string, value []byte) {
	if s.data[namespace] == nil {
		s.data[namespace] = map[string][]byte{}
	}
	s.data[namespace][key] = cloneBytes(value)
}
