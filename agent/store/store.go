// Package store defines persistence contracts for agent sessions.
package store

import (
	"encoding/json"
	"time"

	"github.com/gurcuff91/harness/types"
)

// ── SessionMeta ──────────────────────────────────────────────────────────

// SessionMeta holds all descriptive and runtime state for a session.
// It is persisted by the store and fully restored on ResumeSession.
type SessionMeta struct {
	// Identity — set at creation, immutable
	ID  string `json:"id"`
	CWD string `json:"cwd"`

	// Display
	Name string `json:"name,omitempty"`

	// Runtime state — mutable, persisted on change
	Model    string `json:"model"`             // "provider/model"
	Thinking string `json:"thinking,omitempty"` // thinking level

	// Accumulated stats — persisted after each turn
	Stats types.SessionStats `json:"stats"`

	// Timestamps
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// ── SessionStore ─────────────────────────────────────────────────────────

// SessionStore is one open session — messages + metadata.
// Owned by a Session instance.
type SessionStore interface {
	// Meta returns the current session metadata.
	Meta() SessionMeta

	// UpdateMeta persists metadata changes (model, stats, name, etc.).
	UpdateMeta(meta SessionMeta) error

	// Messages returns all messages for LLM history reconstruction.
	Messages() []json.RawMessage

	// AddMessage appends a message to the log. Thread-safe.
	AddMessage(msg json.RawMessage) error

	// Truncate keeps only the last N messages.
	Truncate(keepLast int) error

	// Close flushes and closes the store.
	Close() error
}

// ── SessionStoreManager ──────────────────────────────────────────────────

// SessionStoreManager creates, opens, and manages sessions.
// Owned by the Agent.
type SessionStoreManager interface {
	// Create opens a new session with the given initial metadata.
	Create(meta SessionMeta) (SessionStore, error)

	// Open restores an existing session by ID.
	// Returns nil (no error) if the session does not exist.
	Open(sessionID string) (SessionStore, error)

	// List returns all sessions for a given working directory.
	List(cwd string) ([]SessionMeta, error)

	// ListAll returns all sessions across all directories.
	ListAll() ([]SessionMeta, error)

	// Delete removes a session permanently.
	Delete(sessionID string) error

	// Rename sets a friendly name for a session.
	Rename(sessionID, name string) error
}
