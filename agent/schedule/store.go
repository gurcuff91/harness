// Package schedule persists and runs cron-scheduled prompts. Schedules are
// stored in ~/.harness/schedules.json, keyed by slug. The agent manages them via
// the Schedule* tools; a transport (e.g. the TUI with --scheduler) runs the
// engine that fires their prompts on time.
package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule is one cron-scheduled prompt. Runs/LastRun are audit fields updated
// by the engine and surfaced to the agent via ScheduleList.
type Schedule struct {
	Slug    string `json:"-"`                  // map key; not stored in the value
	Cron    string `json:"cron"`               // 5-field standard cron expression
	Prompt  string `json:"prompt"`             // the prompt text to run
	Owner   string `json:"owner,omitempty"`    // session id to route the fired prompt to (empty = single-session fallback)
	Runs    int    `json:"runs,omitempty"`     // audit: how many times it has fired
	LastRun int64  `json:"last_run,omitempty"` // audit: Unix ms of the last run
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

// Store is the JSON-backed schedule collection, safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]Schedule // slug → schedule
}

// Open loads the schedule store from path (default ~/.harness/schedules.json
// when empty). A missing file yields an empty store.
func Open(path string) (*Store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("schedule: home dir: %w", err)
		}
		path = filepath.Join(home, ".harness", "schedules.json")
	}
	s := &Store{path: path, data: map[string]Schedule{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	return s, nil
}

// Set upserts a schedule by slug after validating its cron expression. owner is
// the session id the fired prompt is routed to (empty for single-session
// transports). Runs and LastRun are preserved across edits.
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
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.data[slug]
	s.data[slug] = Schedule{
		Cron:    spec,
		Prompt:  prompt,
		Owner:   owner,
		Runs:    existing.Runs, // preserve audit on edit
		LastRun: existing.LastRun,
	}
	return s.save()
}

// Delete removes a schedule by slug. Returns whether it existed.
func (s *Store) Delete(slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[slug]; !ok {
		return false, nil
	}
	delete(s.data, slug)
	return true, s.save()
}

// List returns all schedules sorted by slug (with the Slug field populated).
func (s *Store) List() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Schedule, 0, len(s.data))
	for slug, sc := range s.data {
		sc.Slug = slug
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// Owners returns a slug→owner map for the current schedules. Used to filter a
// listing to a single session's schedules (the owner is not part of the
// agent-facing ScheduleEntry, so it's exposed separately).
func (s *Store) Owners() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.data))
	for slug, sc := range s.data {
		out[slug] = sc.Owner
	}
	return out
}

// RecordRun bumps the audit counters for a slug after the engine fires it.
func (s *Store) RecordRun(slug string, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.data[slug]
	if !ok {
		return nil
	}
	sc.Runs++
	sc.LastRun = at
	s.data[slug] = sc
	return s.save()
}

// save writes the store to disk (caller holds the lock).
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0644)
}
