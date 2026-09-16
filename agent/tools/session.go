// Package tools — SessionInfo + SessionSearch built-in tools.
//
// Two views of the same concept ("information about this session"), gated
// behind the single AgentOptions.EnableSessionInfo flag:
//   - SessionInfo returns a small snapshot of the session's own identity/
//     config (id, cwd, name, model, thinking, created_at).
//   - SessionSearch full-text searches the ENTIRE conversation history
//     (including anything already folded into a compaction checkpoint) via
//     a per-session SQLite FTS5 index synced lazily, entirely inside this
//     tool — no hook into agent/store, no change to AddMessage. See
//     docs/plans/2026-09-16-session-info-search-tools-design.md.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo) — same as agent/memory

	"github.com/gurcuff91/harness/types"
)

// SessionInfoSnapshot is the JSON shape SessionInfo returns. Deliberately
// excludes CompactCount (the model has no existing concept of compaction
// exposed to it), LastActiveAt (redundant for a live session), and all of
// Stats (cost/tokens/context-window usage — operational data that belongs
// in the TUI footer only, matching the project's existing convention of
// never surfacing spend to the model).
type SessionInfoSnapshot struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd"`
	Name      string `json:"name"`
	Model     string `json:"model"`
	Thinking  string `json:"thinking"`
	CreatedAt string `json:"created_at"`
}

// SessionInfoProvider supplies the live snapshot SessionInfo returns — the
// agent injects a closure reading its own Session, keeping this package
// free of any dependency on agent/store's concrete types.
type SessionInfoProvider func() SessionInfoSnapshot

// SessionInfo returns the SessionInfo tool: a no-argument snapshot of the
// session's own identity/config.
func SessionInfo(provider SessionInfoProvider) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSessionInfo,
			Description: "Return a snapshot of THIS session's own identity and configuration: id, working directory, name, active model, and thinking level.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			snap := provider()
			out, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal session info: %w", err)
			}
			return string(out), nil
		},
	}
}

// ── SessionSearch ──────────────────────────────────────────────────────────

// sessionSearchInput is the JSON input schema for the SessionSearch tool.
type sessionSearchInput struct {
	Query string `json:"query" validate:"required"`
	Limit int    `json:"limit,omitempty"`
}

const (
	sessionSearchDefaultLimit = 10
	sessionSearchMaxLimit     = 25
)

// SessionMessageProvider supplies the session's COMPLETE message history
// (including everything before the current compaction checkpoint) each time
// SessionSearch is invoked — the agent injects a closure over its own
// Session.AllMessages(), keeping this package free of any agent/store
// dependency. Called fresh on every search so newly-appended messages are
// always visible without any write-side hook.
type SessionMessageProvider func() []types.Message

// SessionSearchIndexPathResolver returns the ABSOLUTE path SessionSearch's
// per-session FTS5 index should live at for a given session id — the agent
// injects a closure built from store.CwdSlug + store.DefaultSessionsDir (or
// wherever the concrete store's session actually lives), so the index sits
// right next to that session's own .jsonl/.meta.json without agent/tools
// ever importing agent/store directly. A resolver returning "" means "no
// stable on-disk location for this session" (e.g. an in-memory store) —
// SessionSearch falls back to an in-process SQLite ":memory:" database in
// that case, rebuilt from scratch on every call (no persistence possible
// without a real path, but the tool still works).
type SessionSearchIndexPathResolver func() string

// SessionSearch returns the SessionSearch tool: full-text search over the
// session's entire conversation history (plain text only — no tool_call,
// no tool_result, no thinking; see the package doc comment and the design
// doc for why).
func SessionSearch(messages SessionMessageProvider, indexPath SessionSearchIndexPathResolver) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSessionSearch,
			Description: "Full-text search over the complete conversation history of this session — every user and assistant message ever exchanged here. Returns matching snippets, most relevant first. Use this to recover something discussed earlier in this conversation that is no longer visible in the current context.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Search terms."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 25, "default": 10, "description": "Maximum number of results to return."}
				},
				"required": ["query"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args sessionSearchInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if err := requireFields(&args); err != nil {
				return err.Error(), err
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("query is required")
			}
			limit := args.Limit
			switch {
			case limit <= 0:
				limit = sessionSearchDefaultLimit
			case limit > sessionSearchMaxLimit:
				limit = sessionSearchMaxLimit
			}

			var path string
			if indexPath != nil {
				path = indexPath()
			}
			db, err := openSessionSearchDB(path)
			if err != nil {
				return "", fmt.Errorf("open session search index: %w", err)
			}
			defer db.Close()

			all := messages()
			if err := syncSessionSearchIndex(db, all); err != nil {
				return "", fmt.Errorf("sync session search index: %w", err)
			}

			results, err := querySessionSearchIndex(db, args.Query, limit)
			if err != nil {
				return "", fmt.Errorf("session search query: %w", err)
			}
			if results == nil {
				results = []sessionSearchResult{}
			}
			out, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal session search results: %w", err)
			}
			return string(out), nil
		},
	}
}

type sessionSearchResult struct {
	Role    string `json:"role"`
	Snippet string `json:"snippet"`
}

// openSessionSearchDB opens (creating if absent) the per-session FTS5 index.
// path == "" means no stable on-disk location — falls back to an in-process
// ":memory:" database (see SessionSearchIndexPathResolver's doc comment).
func openSessionSearchDB(path string) (*sql.DB, error) {
	dsn := ":memory:"
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create index dir: %w", err)
		}
		dsn = path
	}
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
	return db, nil
}

// syncSessionSearchIndex indexes only the messages that arrived since the
// last sync — the offset is a plain message COUNT (not a byte offset,
// since the source is Session.AllMessages(), not a raw file — see the
// design doc's "ajuste" note), stored in search_meta. Only plain text from
// user/assistant messages is indexed; tool_call/tool_result/thinking parts
// are skipped entirely.
func syncSessionSearchIndex(db *sql.DB, all []types.Message) error {
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

// querySessionSearchIndex runs the FTS5 MATCH query and returns snippets
// (native FTS5 snippet() — a fragment with the match highlighted, not the
// full message), ranked by bm25() relevance, capped to limit.
func querySessionSearchIndex(db *sql.DB, query string, limit int) ([]sessionSearchResult, error) {
	ftsQuery := toSessionSearchFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}
	rows, err := db.Query(`
SELECT role, snippet(messages_fts, 1, '[', ']', '...', 12)
FROM messages_fts
WHERE messages_fts MATCH ?
ORDER BY bm25(messages_fts)
LIMIT ?`, ftsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []sessionSearchResult
	for rows.Next() {
		var r sessionSearchResult
		if err := rows.Scan(&r.Role, &r.Snippet); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// toSessionSearchFTSQuery mirrors agent/memory's toFTSQuery: splits on
// whitespace, quotes+escapes each term, and appends a prefix wildcard so
// partial words still match — kept as an independent copy (not imported
// from agent/memory) since agent/tools must not depend on that package
// either.
func toSessionSearchFTSQuery(raw string) string {
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
