package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// MemoryEntry is one memory as seen by the tools (mirrors memory.Memory,
// redefined here so the tools package doesn't import the memory package).
type MemoryEntry struct {
	Slug      string  `json:"slug"`
	Content   string  `json:"content,omitempty"`
	Score     float64 `json:"score,omitempty"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// MemorySearchResult is a paginated search response.
type MemorySearchResult struct {
	Total    int           `json:"total"`
	Returned int           `json:"returned"`
	Skip     int           `json:"skip"`
	Limit    int           `json:"limit"`
	Results  []MemoryEntry `json:"results"`
}

// MemoryStore is the interface the memory tools use. The concrete SQLite store
// (package memory) is injected by the agent at session creation, keeping the
// tools package free of the storage dependency. All operations are scoped to a
// working directory (cwd) so one project's memories never mix with another's.
type MemoryStore interface {
	Write(cwd, slug, content string) (created bool, err error)
	Search(cwd, query string, includeContent bool, skip, limit int) (MemorySearchResult, error)
	Delete(cwd, slug string) (bool, error)
}

// MemoWrite returns the tool that creates or updates a project memory.
func MemoWrite(store MemoryStore, cwd string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name: ToolMemoWrite,
			Description: "Save a durable, project-scoped memory that persists across future sessions — decisions, conventions, gotchas, architecture notes, anything genuinely worth recalling later. Do NOT save transient task state, trivia, or low-value details. The slug is a short unique id (e.g. \"db-schema\", \"auth-flow\"); reusing a slug overwrites it. Write clear, self-contained content so it is useful when recalled later.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string","description":"Short unique id for the memory (kebab-case, e.g. \"api-auth-flow\")"},"content":{"type":"string","description":"The full memory content to remember"}},"required":["slug","content"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Slug    string `json:"slug"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", fmt.Errorf("MemoWrite: invalid input: %w", err)
			}
			if p.Slug == "" || p.Content == "" {
				return "", fmt.Errorf("MemoWrite: slug and content are required")
			}
			created, err := store.Write(cwd, p.Slug, p.Content)
			if err != nil {
				return "", err
			}
			if created {
				return fmt.Sprintf("Saved memory %q.", p.Slug), nil
			}
			return fmt.Sprintf("Updated memory %q.", p.Slug), nil
		},
	}
}

// MemoSearch returns the tool that recalls memories by full-text query. It
// returns the FULL matching memories (content included) ranked by relevance,
// paginated with skip/limit — so a single call both discovers and reads.
// Response is JSON.
func MemoSearch(store MemoryStore, cwd string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name: ToolMemoSearch,
			Description: "Look up your saved memories for this project. Two modes: (1) with a 'query' — full-text search by keyword or phrase, returns the matching memories ranked by relevance; (2) without a 'query' — lists all memories for this project (most recently updated first). You don't need to know exact slugs. By default each result includes its full 'content'; set 'include_content' to false to get a lightweight list of just slugs and dates — useful to see what exists before pulling specific ones. Paginate with 'skip'/'limit'. Use this to rediscover prior decisions, conventions, and context, especially when you lack context about earlier work.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Keywords or phrase to search for; omit to list all memories"},"include_content":{"type":"boolean","description":"Include each memory's full content (default true); set false for a lightweight slug+dates listing"},"skip":{"type":"integer","description":"Pagination offset (default 0)"},"limit":{"type":"integer","description":"Max results per page (default 10)"}}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Query          string `json:"query"`
				IncludeContent *bool  `json:"include_content"`
				Skip           int    `json:"skip"`
				Limit          int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", fmt.Errorf("MemoSearch: invalid input: %w", err)
			}
			includeContent := true // default
			if p.IncludeContent != nil {
				includeContent = *p.IncludeContent
			}
			res, err := store.Search(cwd, p.Query, includeContent, p.Skip, p.Limit)
			if err != nil {
				return "", err
			}
			out, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				return "", fmt.Errorf("MemoSearch: encode result: %w", err)
			}
			return string(out), nil
		},
	}
}

// MemoDelete returns the tool that removes a memory by slug.
func MemoDelete(store MemoryStore, cwd string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolMemoDelete,
			Description: "Delete a saved memory by its slug when it is no longer relevant or was superseded.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string","description":"The memory slug to delete"}},"required":["slug"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", fmt.Errorf("MemoDelete: invalid input: %w", err)
			}
			ok, err := store.Delete(cwd, p.Slug)
			if err != nil {
				return "", err
			}
			if !ok {
				return fmt.Sprintf("No memory found with slug %q.", p.Slug), nil
			}
			return fmt.Sprintf("Deleted memory %q.", p.Slug), nil
		},
	}
}
