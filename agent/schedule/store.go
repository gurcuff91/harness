// Package schedule runs cron-scheduled prompts. Schedules are persisted
// through harness's settings store (see internal/config's SettingsManager —
// one entry per owner session + slug), so they live wherever settings live:
// ~/.harness/settings.json by default, or any configstore.SettingsStore an
// SDK embedder registered. The agent manages them via the Schedule* tools; a
// transport (e.g. the TUI with --scheduler) runs the engine that fires their
// prompts on time.
package schedule

import (
	"fmt"
	"time"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/robfig/cron/v3"
)

// Schedule is one cron-scheduled prompt. Runs/LastRun are audit fields updated
// by the engine and surfaced to the agent via ScheduleList.
type Schedule struct {
	Slug    string // short id, unique per owner
	Cron    string // 5-field standard cron expression
	Prompt  string // the prompt text to run
	Owner   string // session id the fired prompt is routed to
	Runs    int    // audit: how many times it has fired
	LastRun int64  // audit: Unix ms of the last run
}

// parser accepts standard 5-field cron plus @daily/@hourly/@every descriptors.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// minInterval is the finest schedule the engine can honor — it polls once per
// this interval, and the smallest cron field (minute) is already 1 minute.
const minInterval = time.Minute

// ValidateCron reports whether spec is a valid 5-field cron expression (or a
// supported @descriptor), AND that it doesn't run more often than once a minute.
// Standard 5-field crons can't be sub-minute; only "@every <sub-minute>" can, so
// that's the case we reject. Exposed so the Schedule tool rejects bad input.
func ValidateCron(spec string) error {
	sched, err := parser.Parse(spec)
	if err != nil {
		return err
	}
	if cds, ok := sched.(cron.ConstantDelaySchedule); ok && cds.Delay < minInterval {
		return fmt.Errorf("interval too short: the minimum is 1 minute (got %q)", spec)
	}
	return nil
}

// Store is the schedule collection, a thin typed layer over the
// process-global settings manager. It adds the schedule-specific rules (cron
// validation, keeping Runs/LastRun across edits, never resurrecting a
// deleted schedule) on top of config's typed persistence; storage, locking
// and cross-process atomicity are entirely the settings store's job.
//
// Several harness processes commonly share the same settings store (TUI +
// Telegram + Slack + `serve`), each with its own *Store — every read goes to
// the store, and every read-modify-write runs inside one atomic
// SettingsStore.SwapValue, so the Engine's RecordRun in one process can never
// clobber a Set/Delete from another.
type Store struct {
	settings *config.SettingsManager
}

// Open returns the schedule store, backed by the process-global settings
// manager. It never fails today; the error return is kept for API
// compatibility. Not a singleton — each Agent opens its own *Store, and any
// number of them share the same underlying settings store.
func Open() (*Store, error) {
	return &Store{settings: config.GetSettingsManager()}, nil
}

// openWith returns a Store over a specific manager (tests).
func openWith(m *config.SettingsManager) *Store { return &Store{settings: m} }

// UpdateAction tells UpdateSchedule what to do with the value fn returned.
type UpdateAction int

const (
	// ActionNoop persists nothing — used when fn decides there is nothing to
	// do (e.g. RecordRun finding the schedule was deleted by another process
	// in the meantime: don't resurrect it).
	ActionNoop UpdateAction = iota
	// ActionWrite persists the returned Schedule under (owner, slug).
	ActionWrite
)

// UpdateSchedule is the ONLY read-modify-write entry point for one (owner,
// slug) schedule: it hands fn the freshest stored state and persists fn's
// decision, atomically across processes (config.SwapSchedule →
// SettingsStore.SwapValue). fn returns (next, ActionWrite, nil) to persist
// next, (Schedule{}, ActionNoop, nil) to do nothing, or an error to abort with
// no write. fn must not call back into the store.
func (s *Store) UpdateSchedule(
	owner, slug string,
	fn func(cur Schedule, ok bool) (next Schedule, action UpdateAction, err error),
) error {
	return s.settings.SwapSchedule(owner, slug, func(cur config.ScheduleEntry, ok bool) (config.ScheduleEntry, bool, error) {
		next, action, err := fn(fromEntry(cur), ok)
		if err != nil || action != ActionWrite {
			return config.ScheduleEntry{}, false, err
		}
		return toEntry(next), true, nil
	})
}

// Set upserts a schedule under (owner, slug) after validating the cron
// expression. owner is the session id the fired prompt is routed to (empty for
// single-session transports). Runs and LastRun are preserved across edits.
func (s *Store) Set(slug, spec, prompt, owner string) error {
	if slug == "" {
		return fmt.Errorf("schedule: slug is required")
	}
	if prompt == "" {
		return fmt.Errorf("schedule: prompt is required")
	}
	if err := ValidateCron(spec); err != nil {
		return fmt.Errorf("schedule: invalid cron %q: %w", spec, err)
	}
	return s.UpdateSchedule(owner, slug, func(cur Schedule, ok bool) (Schedule, UpdateAction, error) {
		next := Schedule{Cron: spec, Prompt: prompt}
		if ok {
			next.Runs = cur.Runs // preserve audit on edit
			next.LastRun = cur.LastRun
		}
		return next, ActionWrite, nil
	})
}

// Delete removes the schedule at (owner, slug). Returns whether it existed.
// A direct delete rather than an UpdateSchedule callback: removing never
// needs to inspect current state to decide anything.
func (s *Store) Delete(slug, owner string) (bool, error) {
	return s.settings.DeleteSchedule(owner, slug)
}

// List returns all schedules across all owners, sorted by slug (with Slug and
// Owner populated) — always the store's current state, including what other
// processes wrote. Used by the engine and the server listing endpoint.
func (s *Store) List() []Schedule {
	entries := s.settings.Schedules()
	out := make([]Schedule, 0, len(entries))
	for _, e := range entries {
		out = append(out, fromEntry(e))
	}
	return out
}

// RecordRun bumps the audit counters for (owner, slug) after the engine fires
// it — applied atomically to the freshest stored state, so it can never
// clobber a concurrent edit another process just made. If the schedule was
// deleted in the meantime (!ok), this is a no-op: it must never resurrect a
// deleted schedule just to record a run against it.
func (s *Store) RecordRun(slug, owner string, at int64) error {
	return s.UpdateSchedule(owner, slug, func(cur Schedule, ok bool) (Schedule, UpdateAction, error) {
		if !ok {
			return Schedule{}, ActionNoop, nil
		}
		cur.Runs++
		cur.LastRun = at
		return cur, ActionWrite, nil
	})
}

func fromEntry(e config.ScheduleEntry) Schedule {
	return Schedule{Slug: e.Slug, Cron: e.Cron, Prompt: e.Prompt, Owner: e.Owner, Runs: e.Runs, LastRun: e.LastRun}
}

func toEntry(s Schedule) config.ScheduleEntry {
	return config.ScheduleEntry{Owner: s.Owner, Slug: s.Slug, Cron: s.Cron, Prompt: s.Prompt, Runs: s.Runs, LastRun: s.LastRun}
}
