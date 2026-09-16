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
	"sync"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo) — same as agent/memory

	"github.com/gurcuff91/harness/types"
)

// sessionSearchLocks serializes SessionSearch's Execute against the SAME
// on-disk index path within this process. busy_timeout+WAL (see
// openSessionSearchDB) is enough to make ORDINARY reads/writes on an
// already-created database wait for each other instead of erroring — but it
// does NOT reliably protect the very first migrate (CREATE TABLE/CREATE
// VIRTUAL TABLE) when several goroutines open a brand-new file at once,
// which is exactly what the ReAct loop's parallel tool execution produces
// the first time a session calls SessionSearch more than once concurrently.
// A single in-process mutex per path removes that race outright, since the
// real contention here is always same-process (one session, one index
// file) — unlike agent/memory's store, which is a genuinely shared,
// long-lived, cross-process file. Never locked for the ":memory:" fallback:
// each call there gets its own fully isolated in-process database, so
// there's nothing to serialize.
var (
	sessionSearchLocksMu sync.Mutex
	sessionSearchLocks   = map[string]*sync.Mutex{}
)

func lockSessionSearchIndex(path string) func() {
	if path == "" {
		return func() {}
	}
	sessionSearchLocksMu.Lock()
	l, ok := sessionSearchLocks[path]
	if !ok {
		l = &sync.Mutex{}
		sessionSearchLocks[path] = l
	}
	sessionSearchLocksMu.Unlock()
	l.Lock()
	return l.Unlock
}

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
			unlock := lockSessionSearchIndex(path)
			defer unlock()

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
		// busy_timeout(5000) + journal_mode(WAL): the ReAct loop runs tool
		// calls in PARALLEL, and Execute opens a FRESH *sql.DB handle on
		// every single invocation (unlike agent/memory's long-lived Store),
		// so two SessionSearch calls racing on the SAME session can easily
		// collide on this file. Without busy_timeout the second writer got
		// an instant "database is locked (5) (SQLITE_BUSY)" instead of
		// waiting — identical fix, identical reasoning, as agent/memory's
		// Open() (see that file's comment for the full explanation of why
		// order matters: busy_timeout before the WAL switch). Skipped for
		// ":memory:" — pragmas apply per-connection there and each parallel
		// call already gets its own throwaway in-memory database anyway.
		dsn = path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
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
	if err := resetIndexIfStaleFilter(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("reset stale index: %w", err)
	}
	return db, nil
}

// sessionSearchFilterVersion identifies the message-FILTERING rules baked
// into syncSessionSearchIndex (which messages get indexed at all), not the
// SQL schema shape. Bump it whenever those rules change so an index built
// under the OLD rules gets wiped and fully rebuilt under the new ones —
// otherwise the message-count-offset incremental sync (see
// syncSessionSearchIndex) would never revisit already-indexed messages, and
// an index built before a filtering fix would carry the excluded content
// forever. Bumped from 1 → 2 when compaction-checkpoint and
// system-generated messages (both stored as Role user) started being
// excluded — those had already been indexed as ordinary user messages by
// any pre-existing .search.db and needed a one-time rebuild to purge.
const sessionSearchFilterVersion = 2

// resetIndexIfStaleFilter wipes the index and resets the sync offset back
// to 0 when the on-disk index was built under an OLDER sessionSearchFilterVersion
// than the one this binary now uses — forcing syncSessionSearchIndex to
// reindex everything under the current (correct) filtering rules on the
// very next call. A missing filter_version row means EITHER a genuinely
// brand-new index (nothing to wipe, the reset is a costless no-op) OR an
// index built before this versioning scheme existed at all (the exact
// pre-fix on-disk shape from the field report) — both are safely handled by
// treating "no row" as version 0, always older than any real version, so
// the wipe always runs and the version gets stamped fresh afterward.
func resetIndexIfStaleFilter(db *sql.DB) error {
	var stored int
	err := db.QueryRow(`SELECT value FROM search_meta WHERE key = 'filter_version'`).Scan(&stored)
	switch {
	case err == sql.ErrNoRows:
		stored = 0
	case err != nil:
		return err
	}
	if stored == sessionSearchFilterVersion {
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
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprintf("%d", sessionSearchFilterVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// syncSessionSearchIndex indexes only the messages that arrived since the
// last sync — the offset is a plain message COUNT (not a byte offset,
// since the source is Session.AllMessages(), not a raw file — see the
// design doc's "ajuste" note), stored in search_meta. Only plain text from
// user/assistant messages is indexed; tool_call/tool_result/thinking parts
// are skipped entirely. Also skips synthetic messages the human never
// actually typed — Meta.IsCompaction (the "Previous conversation summary:"
// checkpoint injected by compaction, stored with Role user) and
// Meta.IsSystemGenerated (the max-iterations progress-check prompt, same
// deal) — both would otherwise pollute results with agent-authored noise
// instead of genuine user/assistant conversation.
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

// sessionSearchSnippetMaxChars caps the returned snippet. FTS5's own
// snippet() function is capped by SQLite itself at 64 TOKENS (roughly one
// short sentence) — its 5th argument "must be greater than zero and equal
// to or less than 64" per the FTS5 docs, no way to raise it — which made
// every result nearly useless for recovering real context. Using
// highlight() instead (full column text, match(es) wrapped in brackets)
// and windowing it ourselves in Go trades that hard 64-token ceiling for a
// much roomier, still-bounded character budget.
const sessionSearchSnippetMaxChars = 512

// querySessionSearchIndex runs the FTS5 MATCH query, ranked by bm25()
// relevance and capped to limit. Uses highlight() (full text, matches
// bracketed) rather than snippet() (see sessionSearchSnippetMaxChars) and
// windows the result down to sessionSearchSnippetMaxChars centered on the
// first match — so a match buried deep in a long message is still surfaced
// instead of silently truncated away.
func querySessionSearchIndex(db *sql.DB, query string, limit int) ([]sessionSearchResult, error) {
	ftsQuery := toSessionSearchFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}
	rows, err := db.Query(`
SELECT role, highlight(messages_fts, 1, '[', ']')
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
		r.Snippet = windowSnippet(r.Snippet, sessionSearchSnippetMaxChars)
		out = append(out, r)
	}
	return out, rows.Err()
}

// windowSnippet trims a highlighted (bracket-marked) full message down to
// at most maxChars, centered on the FIRST match so a match buried deep in a
// long message still ends up in the returned window instead of getting cut
// off by a naive head-truncation. Operates on runes (not bytes) so UTF-8
// text is never split mid-character, and nudges each cut to the nearest
// space within a small margin so words aren't chopped in half either.
// Ellipses mark whichever side(s) were actually trimmed.
func windowSnippet(full string, maxChars int) string {
	runes := []rune(full)
	if len(runes) <= maxChars {
		return full
	}

	matchIdx := 0
	for i, r := range runes {
		if r == '[' {
			matchIdx = i
			break
		}
	}

	half := maxChars / 2
	start := matchIdx - half
	if start < 0 {
		start = 0
	}
	end := start + maxChars
	if end > len(runes) {
		end = len(runes)
		start = end - maxChars
		if start < 0 {
			start = 0
		}
	}

	const wordMargin = 40
	prefix, suffix := "", ""
	if start > 0 {
		for j := start; j < start+wordMargin && j < end; j++ {
			if runes[j] == ' ' {
				start = j + 1
				break
			}
		}
		prefix = "..."
	}
	if end < len(runes) {
		for j := end; j > end-wordMargin && j > start; j-- {
			if runes[j-1] == ' ' {
				end = j - 1
				break
			}
		}
		suffix = "..."
	}
	return prefix + string(runes[start:end]) + suffix
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
