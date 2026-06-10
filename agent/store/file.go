package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// ── FileSessionStoreManager ──────────────────────────────────────────────

// FileSessionStoreManager persists sessions to the filesystem.
// Layout:
//
//	~/.harness/agent/sessions/<cwd-slug>/<session-id>.meta.json
//	~/.harness/agent/sessions/<cwd-slug>/<session-id>.jsonl
//
// .meta.json → SessionMeta (rewritten on each UpdateMeta)
// .jsonl     → one types.Message per line, append-only forever
type FileSessionStoreManager struct {
	baseDir string // e.g. ~/.harness/agent/sessions
	mu      sync.Mutex
}

// NewFileSessionStoreManager creates a manager backed by the filesystem.
// baseDir defaults to ~/.harness/agent/sessions if empty.
func NewFileSessionStoreManager(baseDir string) (*FileSessionStoreManager, error) {
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		baseDir = filepath.Join(home, ".harness", "agent", "sessions")
	}
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("create sessions dir: %w", err)
	}
	return &FileSessionStoreManager{baseDir: baseDir}, nil
}

func (m *FileSessionStoreManager) Create(meta SessionMeta) (SessionStore, error) {
	dir := m.sessionDir(meta.CWD)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	metaPath := filepath.Join(dir, meta.ID+".meta.json")
	jsonlPath := filepath.Join(dir, meta.ID+".jsonl")

	// Write initial meta
	if err := writeMetaFile(metaPath, meta); err != nil {
		return nil, fmt.Errorf("write meta: %w", err)
	}
	// Create empty JSONL
	f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("create jsonl: %w", err)
	}
	f.Close()

	return &FileSessionStore{
		metaPath:       metaPath,
		jsonlPath:      jsonlPath,
		meta:           meta,
		messages:       nil,
		diskReadOffset: 0,
		diskWriteCount: 0,
	}, nil
}

func (m *FileSessionStoreManager) Open(sessionID string) (SessionStore, error) {
	// Search all cwd-slug dirs for this session ID
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return nil, nil // no sessions dir yet
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(m.baseDir, e.Name(), sessionID+".meta.json")
		jsonlPath := filepath.Join(m.baseDir, e.Name(), sessionID+".jsonl")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		return openFileSessionStore(metaPath, jsonlPath)
	}
	return nil, nil // not found
}

func (m *FileSessionStoreManager) List(cwd string) ([]SessionMeta, error) {
	dir := m.sessionDir(cwd)
	return readMetasFromDir(dir)
}

func (m *FileSessionStoreManager) ListAll() ([]SessionMeta, error) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return nil, nil
	}
	var all []SessionMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metas, _ := readMetasFromDir(filepath.Join(m.baseDir, e.Name()))
		all = append(all, metas...)
	}
	return all, nil
}

func (m *FileSessionStoreManager) Delete(sessionID string) error {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(m.baseDir, e.Name())
		metaPath := filepath.Join(dir, sessionID+".meta.json")
		jsonlPath := filepath.Join(dir, sessionID+".jsonl")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		os.Remove(metaPath)
		os.Remove(jsonlPath)
		// Remove dir if empty
		if entries, _ := os.ReadDir(dir); len(entries) == 0 {
			os.Remove(dir)
		}
		return nil
	}
	return fmt.Errorf("session %s not found", sessionID)
}

func (m *FileSessionStoreManager) Rename(sessionID, name string) error {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return fmt.Errorf("read sessions: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(m.baseDir, e.Name(), sessionID+".meta.json")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		meta, err := readMetaFile(metaPath)
		if err != nil {
			return err
		}
		meta.Name = name
		return writeMetaFile(metaPath, meta)
	}
	return fmt.Errorf("session %s not found", sessionID)
}

// sessionDir returns the directory for a given cwd, creating a sanitized slug.
func (m *FileSessionStoreManager) sessionDir(cwd string) string {
	return filepath.Join(m.baseDir, cwdSlug(cwd))
}

// cwdSlug converts a cwd path to a filesystem-safe directory name.
// Replaces / with - and spaces with _. Trims leading/trailing separators.
func cwdSlug(cwd string) string {
	// Normalize separators
	slug := strings.ReplaceAll(cwd, string(os.PathSeparator), "-")
	slug = strings.ReplaceAll(slug, " ", "_")
	// Remove leading/trailing dashes
	slug = strings.Trim(slug, "-")
	// Fallback if empty
	if slug == "" {
		slug = "root"
	}
	return slug
}


// ── FileSessionStore ─────────────────────────────────────────────────────

// FileSessionStore is one open session backed by .meta.json + .jsonl.
// Fully independent from InMemorySessionStore — owns its own in-memory cache.
// diskReadOffset: JSONL lines skipped at Open() — translates memory→disk CompactOffset.
// diskWriteCount: messages already on disk — only messages[diskWriteCount:] need appending.
type FileSessionStore struct {
	mu             sync.Mutex
	metaPath       string
	jsonlPath      string
	meta           SessionMeta
	messages       []types.Message
	diskReadOffset int
	diskWriteCount int
}

func openFileSessionStore(metaPath, jsonlPath string) (*FileSessionStore, error) {
	meta, err := readMetaFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}

	absoluteOffset := meta.CompactOffset
	messages, err := readJSONLFrom(jsonlPath, absoluteOffset)
	if err != nil {
		return nil, fmt.Errorf("read jsonl: %w", err)
	}

	// In-memory CompactOffset is always 0 after load — messages[0] IS the checkpoint
	meta.CompactOffset = 0

	return &FileSessionStore{
		metaPath:       metaPath,
		jsonlPath:      jsonlPath,
		meta:           meta,
		messages:       messages,
		diskReadOffset: absoluteOffset,
		diskWriteCount: len(messages),
	}, nil
}

func (s *FileSessionStore) Meta() SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// UpdateMeta updates in-memory state and immediately persists to disk.
func (s *FileSessionStore) UpdateMeta(meta SessionMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta = meta
	diskMeta := meta
	diskMeta.CompactOffset = s.diskReadOffset + s.meta.CompactOffset
	diskMeta.LastActiveAt = time.Now()
	return writeMetaFile(s.metaPath, diskMeta)
}

// Messages returns messages since the last compaction checkpoint.
func (s *FileSessionStore) Messages() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	offset := s.meta.CompactOffset
	slice := s.messages[offset:]
	out := make([]types.Message, len(slice))
	copy(out, slice)
	return out
}

// AllMessages reads the full JSONL from disk (offset 0), returning complete history.
func (s *FileSessionStore) AllMessages() []types.Message {
	s.mu.Lock()
	jsonlPath := s.jsonlPath
	s.mu.Unlock()
	messages, err := readJSONLFrom(jsonlPath, 0)
	if err != nil {
		// Fallback: return in-memory slice
		s.mu.Lock()
		out := make([]types.Message, len(s.messages))
		copy(out, s.messages)
		s.mu.Unlock()
		return out
	}
	return messages
}

// AddMessage appends to in-memory cache only. Flushed to JSONL on Close().
func (s *FileSessionStore) AddMessage(msg types.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

// AddCompactionSummary is a critical write — flushes to disk synchronously.
// Ensures CompactOffset is durable before the session continues.
func (s *FileSessionStore) AddCompactionSummary(summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, CompactionMessage(summary))
	s.meta.CompactOffset = len(s.messages) - 1
	s.meta.CompactCount++

	if err := s.flushToDisk(); err != nil {
		// Rollback
		s.messages = s.messages[:len(s.messages)-1]
		s.meta.CompactOffset = 0
		s.meta.CompactCount--
		return fmt.Errorf("flush after compaction: %w", err)
	}
	return nil
}

// Close flushes all in-memory state to disk.
func (s *FileSessionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushToDisk()
}

// flushToDisk appends new messages to JSONL and writes meta. Must hold s.mu.
func (s *FileSessionStore) flushToDisk() error {
	for _, msg := range s.messages[s.diskWriteCount:] {
		if err := appendToJSONL(s.jsonlPath, msg); err != nil {
			return fmt.Errorf("flush jsonl: %w", err)
		}
	}
	s.diskWriteCount = len(s.messages)

	diskMeta := s.meta
	diskMeta.CompactOffset = s.diskReadOffset + s.meta.CompactOffset
	diskMeta.LastActiveAt = time.Now()
	return writeMetaFile(s.metaPath, diskMeta)
}


func writeMetaFile(path string, meta SessionMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func readMetaFile(path string) (SessionMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionMeta{}, err
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SessionMeta{}, err
	}
	return meta, nil
}

func appendToJSONL(path string, msg types.Message) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

func readJSONLFrom(path string, startLine int) ([]types.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var messages []types.Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line
	lineNum := 0

	for scanner.Scan() {
		if lineNum < startLine {
			lineNum++
			continue
		}
		var msg types.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			lineNum++
			continue // skip malformed lines
		}
		messages = append(messages, msg)
		lineNum++
	}
	return messages, scanner.Err()
}

func readMetasFromDir(dir string) ([]SessionMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var metas []SessionMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		meta, err := readMetaFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		metas = append(metas, meta)
	}
	return metas, nil
}
