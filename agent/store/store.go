// Package store defines the persistence port for agent sessions.
//
// SDK users implement ONE small interface — SessionStore — a dumb, primitive
// key/value + append-log backend. All the harness-specific logic (message
// caching, compaction checkpoints, offset bookkeeping) lives in the *Session
// handle, which harness builds on top of the port. Backends never see any of it.
package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// ── SessionMeta ──────────────────────────────────────────────────────────

// SessionMeta holds all descriptive and runtime state for a session.
// It is persisted by the store and fully restored on resume.
type SessionMeta struct {
	// Identity — set at creation, immutable
	ID  string `json:"id"`
	CWD string `json:"cwd"`

	// Display
	Name string `json:"name,omitempty"`

	// Runtime state — mutable, persisted on change
	Model    string `json:"model"`    // "provider/model"
	Thinking string `json:"thinking"` // thinking level ("off" if disabled)

	// Compaction — CompactOffset is the ABSOLUTE index, in the message log, of
	// the current checkpoint (working set starts here). CompactCount is audit.
	CompactOffset int `json:"compact_offset,omitempty"`
	CompactCount  int `json:"compact_count,omitempty"`

	// Accumulated stats — persisted after each turn
	Stats types.SessionStats `json:"stats"`

	// Timestamps
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// ── SessionStore: the persistence port (what SDK users implement) ─────────

// SessionStore is a minimal, backend-agnostic persistence port. It stores
// session metadata and a flat, append-only message log per session — nothing
// more. Harness layers all session semantics on top via the *Session handle, so
// an implementation only needs to be a dumb store (files, SQLite, Postgres, S3…).
//
// All methods are keyed by sessionID. Implementations must be safe for
// concurrent use.
type SessionStore interface {
	// SaveMeta creates or updates the metadata for a session (upsert by ID).
	SaveMeta(meta SessionMeta) error

	// LoadMeta returns a session's metadata. found is false (no error) when the
	// session does not exist.
	LoadMeta(sessionID string) (meta SessionMeta, found bool, err error)

	// ListMetas returns metadata for sessions in the given working directory,
	// or ALL sessions when cwd is "".
	ListMetas(cwd string) ([]SessionMeta, error)

	// DeleteSession permanently removes a session (its meta and message log).
	DeleteSession(sessionID string) error

	// AppendMessage appends one message to a session's log. The session is
	// created on first append if it doesn't exist yet.
	AppendMessage(sessionID string, msg types.Message) error

	// LoadMessages returns a session's messages starting at fromIndex (an
	// absolute position in the log). fromIndex 0 returns the full history.
	LoadMessages(sessionID string, fromIndex int) ([]types.Message, error)

	// Close releases any backend resources (open files, DB handles, …).
	Close() error
}

// ── Session: the domain handle (owned by harness, built on the port) ──────

// Session is one open conversation, layered over a SessionStore. It caches the
// working set (messages since the last compaction checkpoint) in memory for the
// hot path, appends new messages to the port immediately for durability, and
// keeps the compaction offset bookkeeping here so the backend stays dumb.
//
// This is a concrete type, not an interface: harness always uses it, and SDK
// users never implement it — they only provide the SessionStore beneath it.
type Session struct {
	mu   sync.Mutex
	port SessionStore
	id   string
	meta SessionMeta

	// working holds messages from baseOffset onward (the checkpoint + everything
	// since). baseOffset is the absolute index of working[0] in the full log and
	// always equals meta.CompactOffset.
	working    []types.Message
	baseOffset int
}

// CreateSession registers a new session in the port and returns its handle.
func CreateSession(port SessionStore, meta SessionMeta) (*Session, error) {
	if err := port.SaveMeta(meta); err != nil {
		return nil, err
	}
	return &Session{port: port, id: meta.ID, meta: meta, baseOffset: meta.CompactOffset}, nil
}

// OpenSession restores an existing session from the port. Returns nil (no error)
// if the session does not exist. Only the working set is loaded into memory (from
// the compaction offset), so resuming a long, compacted session stays cheap.
func OpenSession(port SessionStore, sessionID string) (*Session, error) {
	meta, found, err := port.LoadMeta(sessionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	working, err := port.LoadMessages(sessionID, meta.CompactOffset)
	if err != nil {
		return nil, err
	}
	return &Session{
		port:       port,
		id:         sessionID,
		meta:       meta,
		working:    working,
		baseOffset: meta.CompactOffset,
	}, nil
}

// Meta returns the current session metadata.
func (s *Session) Meta() SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// UpdateMeta persists metadata changes (model, stats, name, thinking, …).
func (s *Session) UpdateMeta(meta SessionMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta.LastActiveAt = time.Now()
	s.meta = meta
	return s.port.SaveMeta(meta)
}

// Messages returns the working set — messages since the last compaction
// checkpoint. This is what the LLM sees each turn; served from memory.
func (s *Session) Messages() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]types.Message, len(s.working))
	copy(out, s.working)
	return out
}

// AllMessages returns the complete history, including pre-compaction messages.
// Reads from the port (cold path — display/resume only). Falls back to the
// working set if the port read fails.
func (s *Session) AllMessages() []types.Message {
	s.mu.Lock()
	id := s.id
	s.mu.Unlock()
	msgs, err := s.port.LoadMessages(id, 0)
	if err != nil {
		s.mu.Lock()
		out := make([]types.Message, len(s.working))
		copy(out, s.working)
		s.mu.Unlock()
		return out
	}
	return msgs
}

// AddMessage appends a message to the log — cached in the working set and
// written to the port immediately (durable; survives a crash).
func (s *Session) AddMessage(msg types.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.port.AppendMessage(s.id, msg); err != nil {
		return err
	}
	s.working = append(s.working, msg)
	return nil
}

// AddCompactionSummary appends a checkpoint message and advances the compaction
// offset so Messages() returns only the checkpoint onward. The log is append-only
// — nothing is deleted, so AllMessages() still returns everything.
func (s *Session) AddCompactionSummary(summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ckpt := CompactionMessage(summary)
	if err := s.port.AppendMessage(s.id, ckpt); err != nil {
		return err
	}
	// The checkpoint sits at the end of the log; its absolute index becomes the
	// new working-set base.
	newOffset := s.baseOffset + len(s.working)
	s.working = []types.Message{ckpt}
	s.baseOffset = newOffset
	s.meta.CompactOffset = newOffset
	s.meta.CompactCount++
	s.meta.LastActiveAt = time.Now()
	return s.port.SaveMeta(s.meta)
}

// Close releases the handle. Messages are already durable (append-immediate), so
// this is a no-op; the underlying port is closed by the Agent, not per session.
func (s *Session) Close() error { return nil }

// ── Shared helpers ──────────────────────────────────────────────────────

// CompactionMessage builds the standard user message used as a compaction
// checkpoint. Exported so store implementations and harness agree on the format.
func CompactionMessage(summary string) types.Message {
	msg := types.NewUserTextMessage("Previous conversation summary:\n\n" + summary)
	msg.Meta = &types.MessageMeta{IsCompaction: true}
	return msg
}

// Rename is a convenience over the port: load a session's meta, set its name,
// and save. Kept here so callers don't reimplement the read-modify-write.
func Rename(port SessionStore, sessionID, name string) error {
	meta, found, err := port.LoadMeta(sessionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("session %s not found", sessionID)
	}
	meta.Name = name
	return port.SaveMeta(meta)
}
