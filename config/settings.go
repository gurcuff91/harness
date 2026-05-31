package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// SettingsManager is a thread-safe store for harness settings.
// Backed by ~/.harness/settings.json.
//
// Domain methods: ActiveModel, ThinkingLevel (harness core concerns).
// Generic KV: Get/Set/Delete for provider-specific settings (e.g. "ollama.url").
type SettingsManager struct {
	mu   sync.RWMutex
	path string
	data settingsData
}

type settingsData struct {
	Model    string            `json:"model,omitempty"`
	Thinking string            `json:"thinking,omitempty"`
	Extra    map[string]string `json:"extra,omitempty"` // provider-specific KV
}

func newSettingsManager() *SettingsManager {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness")
	_ = os.MkdirAll(dir, 0700)
	m := &SettingsManager{
		path: filepath.Join(dir, "settings.json"),
		data: settingsData{Extra: make(map[string]string)},
	}
	m.load()
	return m
}

// ── Domain methods ───────────────────────────────────────────────────────

// ActiveModel returns the persisted active model ("provider/model").
func (m *SettingsManager) ActiveModel() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data.Model
}

// SetActiveModel persists the active model.
func (m *SettingsManager) SetActiveModel(model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Model = model
	return m.save()
}

// ThinkingLevel returns the persisted thinking level.
// Falls back to env var HARNESS_THINKING, then empty string.
func (m *SettingsManager) ThinkingLevel() string {
	if v := os.Getenv("HARNESS_THINKING"); v != "" {
		return v
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data.Thinking
}

// SetThinkingLevel persists the thinking level.
func (m *SettingsManager) SetThinkingLevel(level string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Thinking = level
	return m.save()
}

// ── Generic KV — for provider-specific settings ──────────────────────────

// Get retrieves a setting by key.
func (m *SettingsManager) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.data.Extra == nil {
		return "", false
	}
	v, ok := m.data.Extra[key]
	return v, ok
}

// Set persists a setting by key.
func (m *SettingsManager) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Extra == nil {
		m.data.Extra = make(map[string]string)
	}
	m.data.Extra[key] = value
	return m.save()
}

// Delete removes a setting by key.
func (m *SettingsManager) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data.Extra, key)
	return m.save()
}

// ── Internal ─────────────────────────────────────────────────────────────

func (m *SettingsManager) load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &m.data)
	if m.data.Extra == nil {
		m.data.Extra = make(map[string]string)
	}
}

func (m *SettingsManager) save() error {
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}
