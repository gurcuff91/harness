package rpc

import "github.com/gurcuff91/harness/agent/memory"

// The wire types of the `memory/*` requests the binary sends when
// `config.memory` has kind "client". The client implements memory.Store's
// scoping contract (see the protocol reference).

// MemoryWriteParams are the params of memory/write: an upsert by cwd + slug.
type MemoryWriteParams struct {
	// CWD is the project the memory belongs to (ignored when Global).
	CWD string `json:"cwd"`
	// Slug is the memory's short unique id inside its scope.
	Slug string `json:"slug"`
	// Content is the full memory text.
	Content string `json:"content"`
	// Global stores the memory under the "<global>" cwd, visible in every project.
	Global bool `json:"global"`
}

// MemoryWriteResult is the result of memory/write.
type MemoryWriteResult struct {
	// Created is true the first time a cwd + slug is written, false on a rewrite.
	Created bool `json:"created"`
}

// MemoryKeyParams are the params of memory/get and memory/delete.
type MemoryKeyParams struct {
	CWD  string `json:"cwd"`
	Slug string `json:"slug"`
	// Global addresses the "<global>" scope instead of CWD.
	Global bool `json:"global"`
}

// MemoryGetResult is the result of memory/get.
type MemoryGetResult struct {
	// Found is false when there is no such memory (Memory is then ignored).
	Found bool `json:"found"`
	// Memory is the entry, with cwd and content set.
	Memory memory.Memory `json:"memory"`
}

// MemorySearchParams are the params of memory/search. The result is a
// memory.SearchResult.
type MemorySearchParams struct {
	// CWD filters: "" = all projects, "<global>" = only global memories, any
	// other = that project's memories plus the global ones.
	CWD string `json:"cwd"`
	// Query is a full-text query ranked by relevance (score set, higher = more
	// relevant); empty = list mode, most recently updated first (no score).
	Query string `json:"query"`
	// IncludeContent false omits each memory's content.
	IncludeContent bool `json:"include_content"`
	// Skip is the page offset (negative = 0).
	Skip int `json:"skip"`
	// Limit is the page size (0 or negative = 10).
	Limit int `json:"limit"`
}

// MemoryDeleteResult is the result of memory/delete.
type MemoryDeleteResult struct {
	// Deleted is true when a memory existed and was removed.
	Deleted bool `json:"deleted"`
}
