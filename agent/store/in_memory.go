package store

import (
	"sync"

	"github.com/gurcuff91/harness/types"
)

// ── InMemoryStore ─────────────────────────────────────────────────────────

// InMemoryStore is a SessionStore backed entirely by RAM — for tests and the
// SDK's no-persist mode. It implements the primitive port; all session semantics
// live in the *Session handle.
type InMemoryStore struct {
	mu    sync.Mutex
	metas map[string]SessionMeta
	logs  map[string][]types.Message
}

// NewInMemoryStore builds an empty in-memory store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		metas: make(map[string]SessionMeta),
		logs:  make(map[string][]types.Message),
	}
}

func (m *InMemoryStore) SaveMeta(meta SessionMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metas[meta.ID] = meta
	return nil
}

func (m *InMemoryStore) LoadMeta(sessionID string) (SessionMeta, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, ok := m.metas[sessionID]
	return meta, ok, nil
}

func (m *InMemoryStore) ListMetas(cwd string) ([]SessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SessionMeta
	for _, meta := range m.metas {
		if cwd == "" || meta.CWD == cwd {
			out = append(out, meta)
		}
	}
	return out, nil
}

func (m *InMemoryStore) DeleteSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.metas, sessionID)
	delete(m.logs, sessionID)
	return nil
}

func (m *InMemoryStore) AppendMessage(sessionID string, msg types.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs[sessionID] = append(m.logs[sessionID], msg)
	return nil
}

func (m *InMemoryStore) LoadMessages(sessionID string, fromIndex int) ([]types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	log := m.logs[sessionID]
	if fromIndex < 0 {
		fromIndex = 0
	}
	if fromIndex > len(log) {
		fromIndex = len(log) // out-of-range → empty tail (matches file store)
	}
	slice := log[fromIndex:]
	out := make([]types.Message, len(slice))
	copy(out, slice)
	return out, nil
}

func (m *InMemoryStore) Close() error { return nil }
