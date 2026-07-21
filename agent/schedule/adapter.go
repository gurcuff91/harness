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

// Delete removes the schedule only if it belongs to owner. A slug owned by
// another session is treated as absent (false, nil) — sessions can't delete each
// other's schedules.
func (a *ToolAdapter) Delete(slug, owner string) (bool, error) {
	if a.s.Owners()[slug] != owner {
		return false, nil
	}
	return a.s.Delete(slug)
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
