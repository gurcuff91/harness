// Package memory implements persistent, project-scoped memory for the agent,
// backed by a single SQLite database (~/.harness/memory.db) with FTS5 full-text
// search. Memories are partitioned by working directory (cwd) so one project's
// memories never mix with another's — mirroring how sessions are scoped.
//
// The design follows the current SOTA for individual/team-scale agent memory:
// SQLite + FTS5/BM25 gives sub-millisecond keyword search with zero external
// services and a single-file backup, without the cost and complexity of a
// vector database (whose break-even is ~10M entries — far beyond agent scale).
package memory

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// GlobalCWD is the sentinel cwd for memories that are not tied to any project.
// Real cwds are absolute paths (they start with "/"), and the angle brackets
// cannot appear in a filesystem root, so this can never collide with a real
// project directory. The sentinel is encapsulated here — callers pass a `global
// bool` and the store maps it to this value, so no other package hardcodes it.
const GlobalCWD = "<global>"

// Memory is a single stored memory entry.
type Memory struct {
	Slug      string  `json:"slug"`
	CWD       string  `json:"cwd,omitempty"`     // project the memory belongs to
	Content   string  `json:"content,omitempty"` // omitted in lightweight listings
	Score     float64 `json:"score,omitempty"`   // BM25 relevance (search mode only; higher = more relevant)
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// SearchResult is a paginated search response.
type SearchResult struct {
	Total    int      `json:"total"`    // total matches across all pages
	Returned int      `json:"returned"` // matches in this page
	Skip     int      `json:"skip"`     // offset applied
	Limit    int      `json:"limit"`    // limit applied
	Results  []Memory `json:"results"`  // ordered by score (desc)
}

// Store is the SQLite-backed memory store. Safe for concurrent use (SQLite
// serializes writes; the driver handles connection pooling).
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the memory database at the given path. An
// empty path defaults to ~/.harness/agent/memory.db.
func Open(path string) (*Store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("memory: resolve home: %w", err)
		}
		dir := filepath.Join(home, ".harness", "agent")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("memory: create dir: %w", err)
		}
		path = filepath.Join(dir, "memory.db")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("memory: open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the schema: a base table plus an external-content FTS5 index
// kept in sync by triggers.
func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS memories (
    id          INTEGER PRIMARY KEY,
    cwd         TEXT NOT NULL,
    slug        TEXT NOT NULL,
    content     TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE(cwd, slug)
);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    slug, content,
    content='memories', content_rowid='id',
    tokenize='porter unicode61'
);

-- Keep the FTS index in sync with the base table.
CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, slug, content) VALUES (new.id, new.slug, new.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, slug, content) VALUES ('delete', old.id, old.slug, old.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, slug, content) VALUES ('delete', old.id, old.slug, old.content);
    INSERT INTO memories_fts(rowid, slug, content) VALUES (new.id, new.slug, new.content);
END;`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("memory: migrate: %w", err)
	}
	return nil
}

// Write creates or updates a memory (upsert keyed by cwd+slug). Returns whether
// it created a new memory (true) or updated an existing one (false). When global
// is true, the memory is stored under the GlobalCWD sentinel instead of cwd, so
// it surfaces in every project's searches.
func (s *Store) Write(cwd, slug, content string, global bool) (created bool, err error) {
	if global {
		cwd = GlobalCWD
	}
	now := time.Now().UnixMilli()
	// Detect create vs update up front (RowsAffected is unreliable for upserts).
	var exists int
	s.db.QueryRow(`SELECT 1 FROM memories WHERE cwd = ? AND slug = ?`, cwd, slug).Scan(&exists)
	_, err = s.db.Exec(`
INSERT INTO memories (cwd, slug, content, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(cwd, slug) DO UPDATE SET
    content    = excluded.content,
    updated_at = excluded.updated_at`,
		cwd, slug, content, now, now)
	if err != nil {
		return false, fmt.Errorf("memory: write: %w", err)
	}
	return exists == 0, nil
}

// Get returns a single memory by slug within a cwd, or (nil, nil) if absent.
func (s *Store) Get(cwd, slug string) (*Memory, error) {
	row := s.db.QueryRow(`
SELECT slug, content, created_at, updated_at
FROM memories WHERE cwd = ? AND slug = ?`, cwd, slug)
	var m Memory
	err := row.Scan(&m.Slug, &m.Content, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: get: %w", err)
	}
	return &m, nil
}

// Search looks up memories, paginated by skip/limit (limit <= 0 defaults to 10;
// skip < 0 becomes 0). Orthogonal filters:
//   - cwd != "": restrict to that project; cwd == "": across ALL projects.
//   - query != "": FTS5 full-text search over slug/content, ranked by BM25
//     relevance (score set, higher = more relevant); query == "": list mode,
//     most-recently-updated first (no score).
//
// includeContent controls whether each result carries its full content or just
// slug + cwd + dates (a lightweight listing). Every result carries its cwd.
func (s *Store) Search(cwd, query string, includeContent bool, skip, limit int) (SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if skip < 0 {
		skip = 0
	}

	// Build the cwd filter clause + args shared by count and select. A project
	// search always includes global memories (cwd = GlobalCWD) alongside the
	// project's own. For a global-only view the caller passes cwd = GlobalCWD,
	// which makes both sides of the OR identical — no special case needed.
	cwdClause := ""
	var cwdArgs []any
	if cwd != "" {
		cwdClause = " AND (m.cwd = ? OR m.cwd = ?)"
		cwdArgs = []any{cwd, GlobalCWD}
	}

	var total int
	var rows *sql.Rows
	var err error
	searching := query != ""

	if searching {
		countArgs := append([]any{query}, cwdArgs...)
		if err = s.db.QueryRow(`
SELECT COUNT(*) FROM memories_fts f JOIN memories m ON m.id = f.rowid
WHERE memories_fts MATCH ?`+cwdClause, countArgs...).Scan(&total); err != nil {
			return SearchResult{}, fmt.Errorf("memory: search count: %w", err)
		}
		// bm25() is lower-is-better; negate so higher = more relevant.
		queryArgs := append(append([]any{query}, cwdArgs...), limit, skip)
		rows, err = s.db.Query(`
SELECT m.slug, m.cwd, m.content, -bm25(memories_fts) AS score, m.created_at, m.updated_at
FROM memories_fts f
JOIN memories m ON m.id = f.rowid
WHERE memories_fts MATCH ?`+cwdClause+`
ORDER BY bm25(memories_fts)
LIMIT ? OFFSET ?`, queryArgs...)
	} else {
		// List mode. The cwd clause here uses the base table alias `m` too, and
		// likewise folds in global memories.
		listCwd := ""
		if cwd != "" {
			listCwd = " WHERE (m.cwd = ? OR m.cwd = ?)"
		}
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM memories m`+listCwd, cwdArgs...).Scan(&total); err != nil {
			return SearchResult{}, fmt.Errorf("memory: list count: %w", err)
		}
		listArgs := append(append([]any{}, cwdArgs...), limit, skip)
		rows, err = s.db.Query(`
SELECT m.slug, m.cwd, m.content, 0 AS score, m.created_at, m.updated_at
FROM memories m`+listCwd+`
ORDER BY m.updated_at DESC
LIMIT ? OFFSET ?`, listArgs...)
	}
	if err != nil {
		return SearchResult{}, fmt.Errorf("memory: search: %w", err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.Slug, &m.CWD, &m.Content, &m.Score, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return SearchResult{}, fmt.Errorf("memory: scan: %w", err)
		}
		if searching {
			// Scale the tiny bm25 magnitude to a readable value (monotonic).
			m.Score = math.Round(m.Score*1e6*100) / 100
		}
		if !includeContent {
			m.Content = ""
		}
		out = append(out, m)
	}
	return SearchResult{
		Total:    total,
		Returned: len(out),
		Skip:     skip,
		Limit:    limit,
		Results:  out,
	}, rows.Err()
}

// Delete removes a memory by slug within a cwd. Returns whether a row was
// deleted. When global is true, it targets the GlobalCWD sentinel so global
// memories can be removed (symmetric with Write).
func (s *Store) Delete(cwd, slug string, global bool) (bool, error) {
	if global {
		cwd = GlobalCWD
	}
	res, err := s.db.Exec(`DELETE FROM memories WHERE cwd = ? AND slug = ?`, cwd, slug)
	if err != nil {
		return false, fmt.Errorf("memory: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
