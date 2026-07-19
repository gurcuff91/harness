package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gurcuff91/harness/types"
)

// ── FileStore ─────────────────────────────────────────────────────────────

// FileStore persists sessions to the filesystem. Layout:
//
//	<baseDir>/<cwd-slug>/<session-id>.meta.json   ← SessionMeta (rewritten on save)
//	<baseDir>/<cwd-slug>/<session-id>.jsonl        ← one types.Message per line
//
// It implements the primitive SessionStore port: a metadata document plus a flat
// append-only message log per session. All compaction/offset logic lives in the
// *Session handle — this backend just reads and writes.
type FileStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewFileStore creates a filesystem-backed store. baseDir defaults to
// ~/.harness/agent/sessions if empty.
func NewFileStore(baseDir string) (*FileStore, error) {
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
	return &FileStore{baseDir: baseDir}, nil
}

// SaveMeta writes the session's metadata. The cwd-slug directory that holds the
// session is derived from meta.CWD; if a session's meta already lives elsewhere
// (shouldn't happen — CWD is immutable), the old copy is left untouched.
func (m *FileStore) SaveMeta(meta SessionMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := m.sessionDir(meta.CWD)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	return writeMetaFile(filepath.Join(dir, meta.ID+".meta.json"), meta)
}

func (m *FileStore) LoadMeta(sessionID string) (SessionMeta, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, found := m.findMetaPath(sessionID)
	if !found {
		return SessionMeta{}, false, nil
	}
	meta, err := readMetaFile(path)
	if err != nil {
		return SessionMeta{}, false, err
	}
	return meta, true, nil
}

func (m *FileStore) ListMetas(cwd string) ([]SessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cwd != "" {
		return readMetasFromDir(m.sessionDir(cwd))
	}
	// cwd == "" → all sessions across every cwd-slug dir.
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

func (m *FileStore) DeleteSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	metaPath, found := m.findMetaPath(sessionID)
	if !found {
		return fmt.Errorf("session %s not found", sessionID)
	}
	dir := filepath.Dir(metaPath)
	os.Remove(metaPath)
	os.Remove(filepath.Join(dir, sessionID+".jsonl"))
	if entries, _ := os.ReadDir(dir); len(entries) == 0 {
		os.Remove(dir)
	}
	return nil
}

// AppendMessage appends one message to the session's JSONL log (created on first
// write). The session's meta must have been saved first, so its directory and
// path are known.
func (m *FileStore) AppendMessage(sessionID string, msg types.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, found := m.findJSONLPath(sessionID)
	if !found {
		return fmt.Errorf("session %s not found (save meta before appending)", sessionID)
	}
	return appendToJSONL(path, msg)
}

func (m *FileStore) LoadMessages(sessionID string, fromIndex int) ([]types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, found := m.findJSONLPath(sessionID)
	if !found {
		return nil, nil
	}
	return readJSONLFrom(path, fromIndex)
}

func (m *FileStore) Close() error { return nil }

// ── path helpers (must hold m.mu) ─────────────────────────────────────────

// findMetaPath locates a session's .meta.json across all cwd-slug dirs.
func (m *FileStore) findMetaPath(sessionID string) (string, bool) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(m.baseDir, e.Name(), sessionID+".meta.json")
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// findJSONLPath locates a session's .jsonl (next to its .meta.json).
func (m *FileStore) findJSONLPath(sessionID string) (string, bool) {
	mp, ok := m.findMetaPath(sessionID)
	if !ok {
		return "", false
	}
	return filepath.Join(filepath.Dir(mp), sessionID+".jsonl"), true
}

// sessionDir returns the directory for a given cwd (a sanitized slug).
func (m *FileStore) sessionDir(cwd string) string {
	return filepath.Join(m.baseDir, cwdSlug(cwd))
}

// cwdSlug converts a cwd path to a filesystem-safe directory name.
func cwdSlug(cwd string) string {
	slug := strings.ReplaceAll(cwd, string(os.PathSeparator), "-")
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "root"
	}
	return slug
}

// ── file I/O ──────────────────────────────────────────────────────────────

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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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

// readJSONLFrom returns messages from startLine (0-based) to the end.
func readJSONLFrom(path string, startLine int) ([]types.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	if startLine < 0 {
		startLine = 0
	}
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
