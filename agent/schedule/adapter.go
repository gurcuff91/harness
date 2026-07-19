package schedule

import "github.com/gurcuff91/harness/agent/tools"

// ToolAdapter wraps a *Store to satisfy tools.ScheduleStore, translating between
// the storage types and the tools types. This keeps agent/tools free of any
// dependency on this package (and on robfig/cron).
type ToolAdapter struct{ s *Store }

// NewToolAdapter returns an adapter exposing the store as a tools.ScheduleStore.
func NewToolAdapter(s *Store) *ToolAdapter { return &ToolAdapter{s: s} }

func (a *ToolAdapter) Set(slug, cron, prompt string) error { return a.s.Set(slug, cron, prompt) }
func (a *ToolAdapter) Delete(slug string) (bool, error)    { return a.s.Delete(slug) }

func (a *ToolAdapter) Entries() []tools.ScheduleEntry {
	list := a.s.List()
	out := make([]tools.ScheduleEntry, len(list))
	for i, sc := range list {
		out[i] = tools.ScheduleEntry{
			Slug:    sc.Slug,
			Cron:    sc.Cron,
			Prompt:  sc.Prompt,
			Runs:    sc.Runs,
			LastRun: sc.LastRun,
		}
	}
	return out
}
