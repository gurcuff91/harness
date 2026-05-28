// Package store defines persistence contracts for agent sessions.
// A SessionStore creates SessionStoreInstances — one per conversation.
package store

import "encoding/json"

// ── Interfaces ──────────────────────────────────────────────────────────

// SessionStoreInstance is one session's append-only log.
// Messages() returns data for LLM context (after last compaction point).
// Other entries (model_change, thinking_change, compaction) are internal.
type SessionStoreInstance interface {
	// Messages returns all messages for LLM history reconstruction.
	Messages() []json.RawMessage

	// AddMessage appends a message to the log. Thread-safe.
	AddMessage(msg json.RawMessage) error

	// Truncate keeps only the last N messages and inserts a compaction marker.
	Truncate(keepLast int) error

	// Close flushes and closes the store.
	Close() error
}

// SessionStore knows how to create and open SessionStoreInstance objects.
type SessionStore interface {
	Create(sessionID, cwd string) (SessionStoreInstance, error)
	Open(sessionID string) (SessionStoreInstance, error)
}
