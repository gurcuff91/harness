package store

import (
	"fmt"
	"sync"

	"github.com/gurcuff91/harness/types"
)

// ── InMemorySessionStoreManager ─────────────────────────────────────────────────

// InMemorySessionStoreManager keeps everything in memory — for tests and SDK no-persist mode.
type InMemorySessionStoreManager struct {
	mu        sync.Mutex
	instances map[string]*InMemorySessionStore
}

func NewInMemorySessionStoreManager() *InMemorySessionStoreManager {
	return &InMemorySessionStoreManager{instances: make(map[string]*InMemorySessionStore)}
}

func (m *InMemorySessionStoreManager) Create(meta SessionMeta) (SessionStore, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := &InMemorySessionStore{meta: meta}
	m.instances[meta.ID] = inst
	return inst, nil
}

func (m *InMemorySessionStoreManager) Open(sessionID string) (SessionStore, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[sessionID]
	if !ok {
		return nil, nil
	}
	return inst, nil
}

func (m *InMemorySessionStoreManager) List(cwd string) ([]SessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []SessionMeta
	for _, inst := range m.instances {
		if inst.meta.CWD == cwd {
			result = append(result, inst.meta)
		}
	}
	return result, nil
}

func (m *InMemorySessionStoreManager) ListAll() ([]SessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]SessionMeta, 0, len(m.instances))
	for _, inst := range m.instances {
		result = append(result, inst.meta)
	}
	return result, nil
}

func (m *InMemorySessionStoreManager) Delete(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.instances, sessionID)
	return nil
}

func (m *InMemorySessionStoreManager) Rename(sessionID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	inst.mu.Lock()
	inst.meta.Name = name
	inst.mu.Unlock()
	return nil
}

// ── InMemorySessionStore ─────────────────────────────────────────────────

type InMemorySessionStore struct {
	mu       sync.Mutex
	meta     SessionMeta
	messages []types.Message
	// compactOffset lives in meta.CompactOffset — no separate field needed
}

func (s *InMemorySessionStore) Meta() SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

func (s *InMemorySessionStore) UpdateMeta(meta SessionMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta = meta
	return nil
}

// Messages returns only messages since the last compaction checkpoint.
func (s *InMemorySessionStore) Messages() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	offset := s.meta.CompactOffset
	slice := s.messages[offset:]
	out := make([]types.Message, len(slice))
	copy(out, slice)
	return out
}

func (s *InMemorySessionStore) AddMessage(msg types.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

// AddCheckpoint appends the summary as a user message, moves the compact offset
// to that position, and increments the compact count.
// Full history is preserved (append-only — nothing is deleted).
func (s *InMemorySessionStore) AddCheckpoint(summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	summaryMsg := types.NewUserTextMessage(
		"Previous conversation summary:\n\n" + summary,
	)
	s.messages = append(s.messages, summaryMsg)
	// CompactOffset points to the summary — it becomes the first message LLM sees
	s.meta.CompactOffset = len(s.messages) - 1
	s.meta.CompactCount++
	return nil
}

func (s *InMemorySessionStore) Close() error { return nil }
