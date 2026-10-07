// Package memory implements persistent, project-scoped memory for the agent.
// Memories are partitioned by working directory (cwd) so one project's
// memories never mix with another's — mirroring how sessions are scoped.
//
// Store is the persistence port: the agent only ever talks to it, and an SDK
// consumer can plug in any backend via harness.AgentWithMemory. SQLiteStore
// (OpenSQLite) is the default implementation — a single SQLite database
// (~/.harness/agent/memory.db) with FTS5 full-text search. That follows the
// current SOTA for individual/team-scale agent memory: SQLite + FTS5/BM25 gives
// sub-millisecond keyword search with zero external services and a single-file
// backup, without the cost and complexity of a vector database (whose
// break-even is ~10M entries — far beyond agent scale).
package memory

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

// Store is the memory persistence port, configured per agent
// (agent.AgentOptions.Memory / harness.AgentWithMemory). An agent with no
// Store has no memory at all: no Memo* tools, no "## Memory" prompt block.
// The agent that receives a Store owns it — Agent.Close closes it.
//
// Scoping contract every implementation must honor:
//   - Write/Get/Delete with global=true operate under GlobalCWD instead of cwd,
//     so the memory surfaces in every project.
//   - Search's cwd is a filter: "" = across ALL projects; GlobalCWD = global
//     memories only; any other value = that project's memories PLUS the
//     global ones.
//   - Search's query: non-empty = full-text search ranked by relevance (Score
//     set, higher = more relevant); empty = list mode, most-recently-updated
//     first (no Score). limit <= 0 defaults to 10; skip < 0 becomes 0.
//     includeContent=false omits Content (a lightweight listing). Every result
//     carries its CWD; Total counts all matches across pages.
//
// Implementations must be safe for concurrent use: the agent runs tool calls
// in parallel, and several harness processes may share one backend.
type Store interface {
	// Write creates or updates (upsert by cwd+slug) a memory, reporting
	// whether it was newly created.
	Write(cwd, slug, content string, global bool) (created bool, err error)
	// Get returns one memory by cwd+slug (global=true → GlobalCWD), with its
	// CWD and Content set; found is false (no error) when it doesn't exist.
	Get(cwd, slug string, global bool) (m Memory, found bool, err error)
	// Search lists or full-text searches memories, paginated (see the
	// scoping contract above).
	Search(cwd, query string, includeContent bool, skip, limit int) (SearchResult, error)
	// Delete removes a memory by cwd+slug, reporting whether one existed.
	Delete(cwd, slug string, global bool) (deleted bool, err error)
	// Close releases backend resources (DB handles, connections, …).
	Close() error
}

// SQLiteStore must satisfy the port.
var _ Store = (*SQLiteStore)(nil)
