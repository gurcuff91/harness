package admin

import (
	"strings"

	"github.com/gurcuff91/harness/agent/memory"
)

// Memory administers persistent memories through the store given with
// WithMemory. Memories are partitioned by working directory; the cwd of a
// read is a FILTER ("" = every project, memory.GlobalCWD = global only, a path
// = that project plus globals), the cwd of a single-memory call is REQUIRED (a
// path, or memory.GlobalCWD for a global memory).
//
// With no store, reads are empty and writes return ErrNotConfigured.
type Memory struct {
	ad  *Admin
	mem memory.Store
}

func newMemory(ad *Admin, c *options) *Memory { return &Memory{ad: ad, mem: c.memory} }

// Configured reports whether a memory store was given.
func (m *Memory) Configured() bool { return m.mem != nil }

func (m *Memory) none() error { return markf(ErrNotConfigured, "memory is not enabled on this agent") }

// Search lists (query "") or full-text searches memories, paginated.
func (m *Memory) Search(cwd, query string, includeContent bool, skip, limit int) (memory.SearchResult, error) {
	if m.mem == nil {
		return memory.SearchResult{Results: []memory.Memory{}}, nil
	}
	return m.mem.Search(cwd, query, includeContent, skip, limit)
}

// List is Search without a query.
func (m *Memory) List(cwd string, includeContent bool, skip, limit int) (memory.SearchResult, error) {
	return m.Search(cwd, "", includeContent, skip, limit)
}

// scope validates a single-memory request's slug and cwd.
func (m *Memory) scope(cwd, slug string) (global bool, err error) {
	if m.mem == nil {
		return false, m.none()
	}
	if strings.TrimSpace(slug) == "" {
		return false, markf(ErrInvalid, "slug is required")
	}
	if cwd == "" {
		return false, markf(ErrInvalid, `cwd query param is required (a project path, or "`+memory.GlobalCWD+`" for a global memory)`)
	}
	return cwd == memory.GlobalCWD, nil
}

// Get returns one memory (ErrNotFound if absent).
func (m *Memory) Get(cwd, slug string) (memory.Memory, error) {
	global, err := m.scope(cwd, slug)
	if err != nil {
		return memory.Memory{}, err
	}
	mem, found, err := m.mem.Get(cwd, slug, global)
	if err != nil {
		return memory.Memory{}, err
	}
	if !found {
		return memory.Memory{}, markf(ErrNotFound, "memory not found: %s", slug)
	}
	return mem, nil
}

// Put creates or replaces one memory (an upsert by cwd+slug) and returns the
// stored memory and whether it was created. Blank content is ErrUnprocessable.
func (m *Memory) Put(cwd, slug, content string) (mem memory.Memory, created bool, err error) {
	global, err := m.scope(cwd, slug)
	if err != nil {
		return memory.Memory{}, false, err
	}
	if strings.TrimSpace(content) == "" {
		return memory.Memory{}, false, markf(ErrUnprocessable, "content is required")
	}
	created, err = m.mem.Write(cwd, slug, content, global)
	if err != nil {
		return memory.Memory{}, false, err
	}
	mem, _, err = m.mem.Get(cwd, slug, global)
	return mem, created, err
}

// Delete removes one memory (ErrNotFound if absent).
func (m *Memory) Delete(cwd, slug string) error {
	global, err := m.scope(cwd, slug)
	if err != nil {
		return err
	}
	deleted, err := m.mem.Delete(cwd, slug, global)
	if err != nil {
		return err
	}
	if !deleted {
		return markf(ErrNotFound, "memory not found: %s", slug)
	}
	return nil
}
