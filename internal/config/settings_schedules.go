package config

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ── Schedules ────────────────────────────────────────────────────────────
//
// Cron-scheduled prompts, one entry per (owner, slug) — owner is the session
// the prompt fires into, so two sessions can each have a schedule with the
// same slug. Cron validation and the engine that fires them live in
// agent/schedule; this is only typed persistence.
//
// Store layout: namespace "schedules", key = entryKey(owner, slug),
// value = ScheduleEntry (which carries owner+slug itself, so the key is never
// parsed back).

const nsSchedules = "schedules"

// ScheduleEntry is one persisted schedule. Runs/LastRun are audit fields the
// engine updates each time the schedule fires.
type ScheduleEntry struct {
	Owner   string `json:"owner"`              // session id the prompt is routed to
	Slug    string `json:"slug"`               // short id, unique per owner
	Cron    string `json:"cron"`               // 5-field cron or @descriptor
	Prompt  string `json:"prompt"`             // the prompt text to run
	Runs    int    `json:"runs,omitempty"`     // how many times it has fired
	LastRun int64  `json:"last_run,omitempty"` // Unix ms of the last run
}

// Schedules returns every schedule across all owners, sorted by slug then
// owner (a stable order for listings).
func (m *SettingsManager) Schedules() []ScheduleEntry {
	all := listJSON[ScheduleEntry](m.store, nsSchedules)
	out := make([]ScheduleEntry, 0, len(all))
	for _, sc := range all {
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Owner < out[j].Owner
	})
	return out
}

// Schedule returns the schedule at (owner, slug).
func (m *SettingsManager) Schedule(owner, slug string) (ScheduleEntry, bool) {
	var sc ScheduleEntry
	return sc, m.getJSON(nsSchedules, entryKey(owner, slug), &sc)
}

// SwapSchedule is the atomic read-modify-write for one (owner, slug) schedule
// — atomic across processes, via SettingsStore.SwapValue. fn receives the
// current schedule (ok=false if absent) and returns:
//   - next:  the schedule to persist (ignored unless write is true); its
//     Owner/Slug are forced to (owner, slug), so a mistaken value can never
//     land under a different identity
//   - write: whether to persist next at all
//   - err:   fn's own failure — returned unchanged, nothing written
//
// fn must not call back into the manager (it runs while the store holds
// whatever guarantees atomicity).
func (m *SettingsManager) SwapSchedule(
	owner, slug string,
	fn func(cur ScheduleEntry, ok bool) (next ScheduleEntry, write bool, err error),
) error {
	return m.store.SwapValue(nsSchedules, entryKey(owner, slug), func(raw []byte, found bool) ([]byte, bool, error) {
		var cur ScheduleEntry
		ok := found && json.Unmarshal(raw, &cur) == nil
		next, write, err := fn(cur, ok)
		if err != nil || !write {
			return nil, false, err
		}
		next.Owner, next.Slug = owner, slug
		encoded, err := json.Marshal(next)
		if err != nil {
			return nil, false, fmt.Errorf("config: encode schedule %s/%s: %w", owner, slug, err)
		}
		return encoded, true, nil
	})
}

// DeleteSchedule removes the schedule at (owner, slug), reporting whether it
// existed just before the delete. The delete itself is unconditional and
// idempotent (the store has no compare-and-delete): if another process
// deletes or recreates the same schedule in between, the outcome is still
// "it's gone", which is all a delete promises.
func (m *SettingsManager) DeleteSchedule(owner, slug string) (existed bool, err error) {
	key := entryKey(owner, slug)
	if _, found, err := m.store.Get(nsSchedules, key); err != nil || !found {
		return false, err
	}
	return true, m.store.Delete(nsSchedules, key)
}
