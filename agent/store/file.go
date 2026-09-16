package store

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo) — same as agent/memory

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

	// searchMu/searchLocks serialize SearchMessages against the SAME
	// on-disk .search.db WITHIN this process — completely independent of
	// mu above (which guards meta/log I/O for ALL sessions). Scoped
	// per-sessionID, not global, so a slow search on one session's index
	// never blocks any other session's search, append, or meta read.
	//
	// This is required, not optional: busy_timeout(5000)+journal_mode(WAL)
	// on the DSN (see searchDB) is enough to make ORDINARY reads/writes on
	// an ALREADY-CREATED database wait for each other instead of erroring
	// — but it does NOT reliably protect the very first migrate (CREATE
	// TABLE/CREATE VIRTUAL TABLE) when several goroutines race to
	// bootstrap a brand-new index file at once, which is exactly what the
	// ReAct loop's parallel tool execution produces the first time a
	// session calls SessionSearch more than once concurrently. Confirmed
	// live: even with the pragmas already set, N goroutines racing to
	// create a brand-new SQLite file hit "database is locked (SQLITE_BUSY)"
	// 3-8 times per 50 rounds at 2-4-way concurrency. The mutex removes
	// that race outright, since the real contention here is always
	// same-process (one session, one index file).
	searchMu    sync.Mutex
	searchLocks map[string]*sync.Mutex
}

// DefaultSessionsDir returns the default base directory FileStore uses when
// constructed with an empty baseDir (~/.harness/agent/sessions). Exported so
// callers that need to locate a session's on-disk directory independently
// of a live FileStore instance — e.g. agent/tools' SessionSearch resolver,
// which must work out the SAME path FileStore would have used even when it
// only has a sessionID/cwd, not a *FileStore handle — can agree on the same
// default without duplicating it.
func DefaultSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".harness", "agent", "sessions"), nil
}

// NewFileStore creates a filesystem-backed store. baseDir defaults to
// ~/.harness/agent/sessions if empty.
func NewFileStore(baseDir string) (*FileStore, error) {
	if baseDir == "" {
		dir, err := DefaultSessionsDir()
		if err != nil {
			return nil, err
		}
		baseDir = dir
	}
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("create sessions dir: %w", err)
	}
	return &FileStore{baseDir: baseDir, searchLocks: map[string]*sync.Mutex{}}, nil
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

// CopyMessages duplicates the JSONL log of srcID into dstID byte-for-byte.
// If srcID has no log yet (never written), dstID gets an empty file.
func (m *FileStore) CopyMessages(srcID, dstID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	dstPath, found := m.findJSONLPath(dstID)
	if !found {
		return fmt.Errorf("CopyMessages: destination session %s not found", dstID)
	}

	srcPath, srcFound := m.findJSONLPath(srcID)
	if !srcFound {
		// Source has no JSONL yet — create an empty file for dst and return.
		f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		return f.Close()
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("CopyMessages: read src: %w", err)
	}
	return os.WriteFile(dstPath, data, 0600)
}

// TruncateMessages empties the session's JSONL log. If the file doesn't exist
// yet, the call is a no-op (nothing to truncate).
func (m *FileStore) TruncateMessages(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, found := m.findJSONLPath(sessionID)
	if !found {
		return nil // no file = already empty
	}
	if err := os.Truncate(path, 0); err != nil {
		return err
	}
	// The search index's message-count offset would otherwise point at
	// content that no longer exists — deleting it and letting the next
	// SearchMessages call rebuild from scratch is simpler and more
	// obviously correct than reconciling a stale offset against a
	// truncated log. Best-effort: a missing index (never searched) or a
	// failed removal isn't fatal to the reset itself.
	m.deleteSearchIndex(sessionID, filepath.Dir(path))
	return nil
}

// deleteSearchIndex removes sessionID's .search.db and its WAL/SHM
// sidecars (if any) from dir. Best-effort — errors are ignored, matching
// the tolerance-to-missing-file behavior the rest of this store already
// has for optional/derived on-disk artifacts.
func (m *FileStore) deleteSearchIndex(sessionID, dir string) {
	base := filepath.Join(dir, sessionID+".search.db")
	os.Remove(base)
	os.Remove(base + "-wal")
	os.Remove(base + "-shm")
}

func (m *FileStore) Close() error { return nil }

// ── SearchMessages: per-session FTS5 index ─────────────────────────────────
//
// Full-text search over a session's complete message history, backed by a
// per-session SQLite FTS5 index living right next to that session's own
// .jsonl/.meta.json: <cwd-slug>/<session-id>.search.db. Built and synced
// LAZILY — nothing is written here until the first SearchMessages call for
// a given session, and only the messages that arrived since the last call
// are indexed on each subsequent one (an incremental sync by message COUNT
// offset, stored in the index's own search_meta table — not a byte offset,
// since the source is LoadMessages, not a raw file read).

// searchIndexLock returns an unlock func for the per-sessionID mutex
// guarding sessionID's .search.db (see FileStore.searchLocks' doc comment
// on the struct for why this is a SEPARATE, per-session lock rather than
// reusing m.mu).
func (m *FileStore) searchIndexLock(sessionID string) func() {
	m.searchMu.Lock()
	l, ok := m.searchLocks[sessionID]
	if !ok {
		l = &sync.Mutex{}
		m.searchLocks[sessionID] = l
	}
	m.searchMu.Unlock()
	l.Lock()
	return l.Unlock
}

func (m *FileStore) SearchMessages(sessionID string, query string, limit int) ([]SearchResult, error) {
	unlock := m.searchIndexLock(sessionID)
	defer unlock()

	m.mu.Lock()
	jsonlPath, found := m.findJSONLPath(sessionID)
	m.mu.Unlock()
	if !found {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	indexPath := filepath.Join(filepath.Dir(jsonlPath), sessionID+".search.db")

	db, err := openSearchIndexDB(indexPath)
	if err != nil {
		return nil, fmt.Errorf("open session search index: %w", err)
	}
	defer db.Close()

	all, err := m.LoadMessages(sessionID, 0)
	if err != nil {
		return nil, fmt.Errorf("load messages for search: %w", err)
	}
	if err := syncSearchIndex(db, all); err != nil {
		return nil, fmt.Errorf("sync session search index: %w", err)
	}

	results, err := querySearchIndex(db, query, limit)
	if err != nil {
		return nil, fmt.Errorf("session search query: %w", err)
	}
	return results, nil
}

// openSearchIndexDB opens (creating if absent) the per-session FTS5 index
// at path. busy_timeout(5000)+journal_mode(WAL) on the DSN handles ordinary
// read/write contention on an already-created index (see FileStore's
// searchLocks doc comment on the struct for why an ADDITIONAL in-process
// mutex is still required around the very first migration).
func openSearchIndexDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // modernc.org/sqlite: single-writer discipline, same as agent/memory
	const schema = `
CREATE TABLE IF NOT EXISTS search_meta(key TEXT PRIMARY KEY, value TEXT);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(role, text, tokenize='unicode61');`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := resetSearchIndexIfStaleFilter(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("reset stale index: %w", err)
	}
	return db, nil
}

// searchIndexFilterVersion identifies the message-FILTERING rules baked
// into syncSearchIndex (which messages get indexed at all), not the SQL
// schema shape. Bump it whenever those rules change so an index built
// under the OLD rules gets wiped and fully rebuilt under the new ones —
// otherwise the message-count-offset incremental sync would never revisit
// already-indexed messages, and an index built before a filtering fix
// would carry the excluded content forever. Currently 2 (unchanged from
// when this lived in agent/tools): compaction-checkpoint and
// system-generated messages (both stored as Role user) are excluded.
const searchIndexFilterVersion = 2

// resetSearchIndexIfStaleFilter wipes the index and resets the sync offset
// back to 0 when the on-disk index was built under an OLDER
// searchIndexFilterVersion than the one this binary now uses — forcing
// syncSearchIndex to reindex everything under the current (correct)
// filtering rules on the very next call. A missing filter_version row
// means EITHER a genuinely brand-new index (nothing to wipe, the reset is
// a costless no-op) OR an index built before this versioning scheme
// existed at all — both are safely handled by treating "no row" as version
// 0, always older than any real version, so the wipe always runs and the
// version gets stamped fresh afterward.
func resetSearchIndexIfStaleFilter(db *sql.DB) error {
	var stored int
	err := db.QueryRow(`SELECT value FROM search_meta WHERE key = 'filter_version'`).Scan(&stored)
	switch {
	case err == sql.ErrNoRows:
		stored = 0
	case err != nil:
		return err
	}
	if stored == searchIndexFilterVersion {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM messages_fts`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM search_meta WHERE key = 'last_indexed_count'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO search_meta(key, value) VALUES ('filter_version', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprintf("%d", searchIndexFilterVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// syncSearchIndex indexes only the messages that arrived since the last
// sync — the offset is a plain message COUNT, stored in search_meta. Only
// plain text from user/assistant messages is indexed; tool_call/
// tool_result/thinking parts are skipped entirely. Also skips synthetic
// messages the human never actually typed — Meta.IsCompaction (the
// "Previous conversation summary:" checkpoint injected by compaction,
// stored with Role user) and Meta.IsSystemGenerated (the max-iterations
// progress-check prompt, same deal) — both would otherwise pollute results
// with agent-authored noise instead of genuine user/assistant conversation.
func syncSearchIndex(db *sql.DB, all []types.Message) error {
	var offsetStr string
	err := db.QueryRow(`SELECT value FROM search_meta WHERE key = 'last_indexed_count'`).Scan(&offsetStr)
	offset := 0
	if err == nil {
		fmt.Sscanf(offsetStr, "%d", &offset)
	} else if err != sql.ErrNoRows {
		return err
	}
	if offset >= len(all) {
		return nil // nothing new
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(`INSERT INTO messages_fts(role, text) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, msg := range all[offset:] {
		if msg.Role != types.RoleUser && msg.Role != types.RoleAssistant {
			continue
		}
		if msg.Meta != nil && (msg.Meta.IsCompaction || msg.Meta.IsSystemGenerated) {
			continue
		}
		var b strings.Builder
		for _, p := range msg.Parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(p.Text)
			}
		}
		text := b.String()
		if text == "" {
			continue // e.g. a tool-call-only assistant message — nothing to index
		}
		if _, err := stmt.Exec(string(msg.Role), text); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`
INSERT INTO search_meta(key, value) VALUES ('last_indexed_count', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprintf("%d", len(all))); err != nil {
		return err
	}
	return tx.Commit()
}

// searchSnippetMaxTokens is FTS5's own hard ceiling for the native
// snippet() function's token-count argument — its 5th argument "must be
// greater than zero and equal to or less than 64" per the FTS5 docs
// (confirmed live against modernc.org/sqlite). 64 is the maximum allowed,
// used here deliberately for the roomiest snippet SQLite's native function
// can produce.
const searchSnippetMaxTokens = 64

// querySearchIndex runs the FTS5 MATCH query, ranked by bm25() relevance
// and capped to limit. Uses FTS5's native snippet() — SQLite's own
// match-centered fragment extraction — rather than a hand-rolled
// alternative: simpler and good enough despite the 64-token ceiling.
func querySearchIndex(db *sql.DB, query string, limit int) ([]SearchResult, error) {
	ftsQuery := toSearchFTSQuery(query)
	if ftsQuery == "" {
		return []SearchResult{}, nil
	}
	rows, err := db.Query(fmt.Sprintf(`
SELECT role, snippet(messages_fts, 1, '[', ']', '...', %d)
FROM messages_fts
WHERE messages_fts MATCH ?
ORDER BY bm25(messages_fts)
LIMIT ?`, searchSnippetMaxTokens), ftsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Role, &r.Snippet); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// toSearchFTSQuery mirrors agent/memory's toFTSQuery: splits on whitespace,
// quotes+escapes each term, and appends a prefix wildcard so partial words
// still match — kept as an independent copy (not imported from
// agent/memory) since agent/store must not depend on that package either.
func toSearchFTSQuery(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	terms := make([]string, len(fields))
	for i, f := range fields {
		terms[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"*`
	}
	return strings.Join(terms, " ")
}

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
	return filepath.Join(m.baseDir, CwdSlug(cwd))
}

// windowsIllegalDirChars are the characters NTFS forbids in a path
// component, MINUS the backslash and forward slash (already handled as
// path separators below) — notably the drive-letter colon ("C:"), which a
// Windows cwd always carries and which sessionDir would otherwise fold
// straight into the directory NAME (not just its natural drive-separator
// position), producing an invalid component like "C:-Users-..." that
// mkdir rejects outright (reported live on Windows: "The directory name
// is invalid."). Stripped unconditionally, not just on GOOS=="windows" —
// a slug must be safe to recreate on ANY platform a session store might
// later be copied to or read from, and none of these characters are
// legal (or even common) in a real Unix path component either.
const windowsIllegalDirChars = `:<>"|?*`

// CwdSlug converts a cwd path to a filesystem-safe directory name — the
// exact same slug FileStore uses to lay out
// <baseDir>/<cwd-slug>/<session-id>.{meta.json,jsonl}. Exported so callers
// outside this package that need to locate a session's ON-DISK directory
// for a purpose FileStore itself doesn't cover (e.g. agent/tools'
// SessionSearch placing its own per-session search index right next to the
// session's .jsonl) can compute the identical path without agent/tools
// importing agent/store directly — the agent constructs a small resolver
// closure using this function and injects it into the tool, the same
// pattern FetchSummarizer already uses to cross that same boundary.
func CwdSlug(cwd string) string {
	slug := strings.ReplaceAll(cwd, "/", "-")
	slug = strings.ReplaceAll(slug, `\`, "-")
	slug = strings.Map(func(r rune) rune {
		if strings.ContainsRune(windowsIllegalDirChars, r) {
			return -1
		}
		return r
	}, slug)
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

	// Guard against a file that doesn't end with '\n' — if the previous write
	// was interrupted (crash, concurrent open, etc.) the next entry would be
	// concatenated onto the same line, producing two JSON objects on one line
	// which shifts all subsequent line-number offsets by one and corrupts the
	// compact_offset stored in metadata.
	//
	// Seek to the end and check the last byte; emit a '\n' if it's missing.
	// This is a no-op on a healthy file (already ends with '\n').
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		if _, err := f.Seek(-1, 2); err == nil {
			last := make([]byte, 1)
			if _, err := f.Read(last); err == nil && last[0] != '\n' {
				_, _ = f.Write([]byte{'\n'}) // repair missing newline
			}
			// Seek back to the end for the actual write.
			_, _ = f.Seek(0, 2)
		}
	}

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
