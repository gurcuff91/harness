package schedule

import "github.com/gurcuff91/harness/agent/tools"

// ToolAdapter wraps a *Store to satisfy tools.ScheduleStore, translating between
// the storage types and the tools types. This keeps agent/tools free of any
// dependency on this package (and on robfig/cron).
type ToolAdapter struct{ s *Store }

// NewToolAdapter returns an adapter exposing the store as a tools.ScheduleStore.
func NewToolAdapter(s *Store) *ToolAdapter { return &ToolAdapter{s: s} }

func (a *ToolAdapter) Set(slug, cron, prompt, owner string) error {
	return a.s.Set(slug, cron, prompt, owner)
}

// Delete removes the schedule only if it belongs to owner. Because the store is
// now keyed by (owner, slug), a slug from another session is simply absent —
// cross-session deletion is structurally impossible, not just policy-blocked.
func (a *ToolAdapter) Delete(slug, owner string) (bool, error) {
	return a.s.Delete(slug, owner)
}

// Entries returns only the schedules owned by owner.
func (a *ToolAdapter) Entries(owner string) []tools.ScheduleEntry {
	var out []tools.ScheduleEntry
	for _, sc := range a.s.List() {
		if sc.Owner != owner {
			continue
		}
		out = append(out, tools.ScheduleEntry{
			Slug:    sc.Slug,
			Cron:    sc.Cron,
			Prompt:  sc.Prompt,
			Runs:    sc.Runs,
			LastRun: sc.LastRun,
		})
	}
	return out
}
