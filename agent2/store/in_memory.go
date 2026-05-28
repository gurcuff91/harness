package store

import (
	"encoding/json"
	"sync"
)

// ── InMemoryStore ───────────────────────────────────────────────────────

// InMemoryStore is a test/development SessionStore that keeps everything in memory.
type InMemoryStore struct {
	mu       sync.Mutex
	sessions map[string]*InMemoryInstance
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{sessions: make(map[string]*InMemoryInstance)}
}

func (s *InMemoryStore) Create(sessionID, cwd string) (SessionStoreInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst := &InMemoryInstance{}
	s.sessions[sessionID] = inst
	return inst, nil
}

func (s *InMemoryStore) Open(sessionID string) (SessionStoreInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	return inst, nil
}

// ── InMemoryInstance ────────────────────────────────────────────────────

type InMemoryInstance struct {
	mu       sync.Mutex
	messages []json.RawMessage
}

func (i *InMemoryInstance) Messages() []json.RawMessage {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]json.RawMessage, len(i.messages))
	copy(out, i.messages)
	return out
}

func (i *InMemoryInstance) AddMessage(msg json.RawMessage) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.messages = append(i.messages, msg)
	return nil
}

func (i *InMemoryInstance) Truncate(keepLast int) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if keepLast >= len(i.messages) {
		return nil
	}
	cut := len(i.messages) - keepLast
	i.messages = i.messages[cut:]
	return nil
}

func (i *InMemoryInstance) Close() error { return nil }
