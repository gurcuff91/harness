package memory

import "github.com/gurcuff91/harness/agent/tools"

// ToolAdapter wraps a *Store to satisfy tools.MemoryStore, translating between
// the storage types and the tools types. This keeps the agent/tools package
// free of any dependency on this package.
type ToolAdapter struct{ s *Store }

// NewToolAdapter returns an adapter exposing the store as a tools.MemoryStore.
func NewToolAdapter(s *Store) *ToolAdapter { return &ToolAdapter{s: s} }

func (a *ToolAdapter) Write(cwd, slug, content string) (bool, error) {
	return a.s.Write(cwd, slug, content)
}

func (a *ToolAdapter) Search(cwd, query string, includeContent bool, skip, limit int) (tools.MemorySearchResult, error) {
	r, err := a.s.Search(cwd, query, includeContent, skip, limit)
	if err != nil {
		return tools.MemorySearchResult{}, err
	}
	out := tools.MemorySearchResult{
		Total:    r.Total,
		Returned: r.Returned,
		Skip:     r.Skip,
		Limit:    r.Limit,
		Results:  make([]tools.MemoryEntry, len(r.Results)),
	}
	for i, m := range r.Results {
		out.Results[i] = tools.MemoryEntry{
			Slug:      m.Slug,
			Content:   m.Content,
			Score:     m.Score,
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		}
	}
	return out, nil
}

func (a *ToolAdapter) Delete(cwd, slug string) (bool, error) {
	return a.s.Delete(cwd, slug)
}
