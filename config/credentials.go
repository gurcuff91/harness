package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CredentialsManager is a thread-safe key-value store for provider credentials.
// Backed by ~/.harness/credentials.json.
// Keys are namespaced by provider: "anthropic.api_key", "claude-oauth.access_token", etc.
type CredentialsManager struct {
	mu   sync.RWMutex
	path string
	data map[string]string
}

func newCredentialsManager() *CredentialsManager {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness")
	_ = os.MkdirAll(dir, 0700)
	m := &CredentialsManager{
		path: filepath.Join(dir, "credentials.json"),
		data: make(map[string]string),
	}
	m.load()
	return m
}

// Store persists a credential value by key.
func (m *CredentialsManager) Store(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return m.save()
}

// Load retrieves a credential value by key.
// Returns ("", false) if the key does not exist.
func (m *CredentialsManager) Load(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	return v, ok
}

// Delete removes a credential by key.
func (m *CredentialsManager) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return m.save()
}

// DeletePrefix removes all credentials whose keys start with prefix.
func (m *CredentialsManager) DeletePrefix(prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			delete(m.data, k)
		}
	}
	return m.save()
}

func (m *CredentialsManager) load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &m.data)
}

func (m *CredentialsManager) save() error {
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}
