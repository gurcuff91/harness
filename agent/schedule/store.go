// Package schedule persists and runs cron-scheduled prompts. Schedules are
// stored in ~/.harness/schedules.json, keyed by owner (session ID) then slug.
// The agent manages them via the Schedule* tools; a transport (e.g. the TUI
// with --scheduler) runs the engine that fires their prompts on time.
package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/robfig/cron/v3"
)

// Schedule is one cron-scheduled prompt. Runs/LastRun are audit fields updated
// by the engine and surfaced to the agent via ScheduleList.
type Schedule struct {
	Slug    string `json:"-"`                  // map key; not stored in the value
	Cron    string `json:"cron"`               // 5-field standard cron expression
	Prompt  string `json:"prompt"`             // the prompt text to run
	Owner   string `json:"-"`                  // outer map key (session id); not stored in value
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

// Store is the JSON-backed schedule collection, safe for concurrent use WITHIN
// one process (s.mu) AND across processes (the file lock — see
// UpdateSchedule/Delete/reloadIfStale). harness commonly runs several
// processes at once against the same schedules.json: the agent ALWAYS opens
// this store so the Schedule* tools work in any session, while EnableScheduler
// only decides whether THIS agent also runs the Engine that fires due
// prompts — so TUI + Telegram + Slack + `serve` sharing one file, each with
// its own in-process *Store, is the documented deployment shape, not an edge
// case. Without the protections below, the Engine's RecordRun (every 30s in
// whichever process has --scheduler) could silently lose a concurrent Set/
// Delete from another process, or vice versa — the same failure mode
// credentials.json had before it was hardened (see
// docs/plans/2026-08-13-oauth-refresh-lock-hardening-design.md and this
// store's own docs/plans/2026-08-17-schedule-store-lock-hardening-design.md).
//
// On disk the layout is:
//
//	{ "<owner-session-id>": { "<slug>": { "cron": "...", "prompt": "...", ... } } }
//
// Keying by owner first means two sessions can each have a schedule with the
// same slug without any collision — the composite key (owner, slug) is unique.
type Store struct {
	mu       sync.Mutex
	path     string
	data     map[string]map[string]Schedule // owner → (slug → schedule)
	loadedAt time.Time                       // mtime of the file as of the last load() — see reloadIfStale
}

// Open loads the schedule store from path (default ~/.harness/schedules.json
// when empty). A missing file yields an empty store. Not a singleton — unlike
// config.CredentialsManager/SettingsManager, there is no existing global
// accessor to preserve, and each Agent deliberately opens its own *Store (one
// per agent instance, potentially several per process for subagents/tests).
// The cross-process lock and reload below work correctly regardless of how
// many *Store instances point at the same file.
func Open(path string) (*Store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("schedule: home dir: %w", err)
		}
		path = filepath.Join(home, ".harness", "schedules.json")
	}
	s := &Store{path: path, data: map[string]map[string]Schedule{}}
	s.load() // populates loadedAt too — see load's comment
	return s, nil
}

// ── Cross-process freshness ───────────────────────────────────────────────
//
// Same problem, same fix as config.CredentialsManager.reloadIfStale (see its
// comment for the full story): s.mu only guards goroutines within THIS
// process. reloadIfStale is called at the top of List() so every reader (the
// Engine's every-30s tick, the ScheduleList tool via ToolAdapter, the server's
// read endpoint) sees what another process most recently wrote, not a
// startup-time snapshot. os.Stat is far cheaper than the full os.ReadFile +
// json.Unmarshal a load() does, so this barely costs anything when the file
// hasn't changed.
func (s *Store) reloadIfStale() {
	info, err := os.Stat(s.path)
	if err != nil {
		return // no file yet — nothing to reload
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if info.ModTime().After(s.loadedAt) {
		s.load()
	}
}

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
// slug) schedule. It takes the cross-process file lock EXACTLY ONCE, reloads
// the freshest on-disk state, and calls fn(current, ok) to decide the
// outcome — fn returns (next, ActionWrite, nil) to persist next, or
// (Schedule{}, ActionNoop, nil) to do nothing, or an error to abort with no
// write. Both callers (Set, RecordRun) share this single path; there is no
// lower-level "give me the lock" primitive exposed, so a caller's fn can never
// re-acquire the lock and deadlock — the same design this replaced in
// config.CredentialsManager (see UpdateCredential's doc comment for that
// history).
//
// Delete is deliberately NOT expressed through this callback — see Delete's
// own doc comment for why.
func (s *Store) UpdateSchedule(
	owner, slug string,
	fn func(cur Schedule, ok bool) (next Schedule, action UpdateAction, err error),
) error {
	release, err := config.AcquireFileLock(s.path)
	if err != nil {
		return err
	}
	defer release()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.load() // the freshest state — another process may have written since we last synced

	cur, ok := Schedule{}, false
	if slugs := s.data[owner]; slugs != nil {
		cur, ok = slugs[slug]
	}

	next, action, fnErr := fn(cur, ok)
	if fnErr != nil {
		return fnErr
	}
	if action != ActionWrite {
		return nil
	}
	if s.data[owner] == nil {
		s.data[owner] = map[string]Schedule{}
	}
	s.data[owner][slug] = next
	return s.save()
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
//
// Kept as a direct method rather than going through UpdateSchedule's callback:
// a delete never needs to INSPECT current state to decide anything — the
// caller's intent is unconditional ("remove it if it's mine"), unlike Set
// (must preserve Runs/LastRun from cur) or RecordRun (must increment fields
// read fresh from cur). Routing it through a decision callback would be fake
// generality for a case with no real decision — and avoids adding a 3rd
// UpdateAction (ActionDelete) that Set/RecordRun would never use, or a second
// bool return whose "write=true AND delete=true" combination would be
// representable-but-invalid.
func (s *Store) Delete(slug, owner string) (bool, error) {
	release, err := config.AcquireFileLock(s.path)
	if err != nil {
		return false, err
	}
	defer release()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.load() // the freshest state — see UpdateSchedule's comment

	if _, ok := s.data[owner][slug]; !ok {
		return false, nil
	}
	delete(s.data[owner], slug)
	if len(s.data[owner]) == 0 {
		delete(s.data, owner) // prune empty owner bucket
	}
	return true, s.save()
}

// List returns all schedules across all owners, sorted by slug (with Slug and
// Owner fields populated). Used by the engine and the server listing endpoint.
// Reloads the freshest disk state first (see reloadIfStale) — a caller here
// never sees a startup-time-stale snapshot when another process has written
// since.
func (s *Store) List() []Schedule {
	s.reloadIfStale()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Schedule
	for owner, slugs := range s.data {
		for slug, sc := range slugs {
			sc.Slug = slug
			sc.Owner = owner
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// RecordRun bumps the audit counters for (owner, slug) after the engine fires
// it. Goes through UpdateSchedule so the increment applies to the FRESHEST
// on-disk state, never a possibly-stale in-memory copy — the fix for the bug
// where the Engine's every-30s tick could clobber a concurrent edit another
// process just made to this (or another) schedule. If the schedule was
// deleted by another process in the meantime (!ok), this is a no-op — it must
// never resurrect a deleted schedule just to record a run against it.
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

// load reads schedules.json from disk into s.data and records the file's mtime
// (at the moment of the read) in s.loadedAt — the baseline reloadIfStale
// compares future os.Stat calls against. Caller holds s.mu (or is Open,
// before any concurrent access is possible). A missing/unreadable file leaves
// s.data as an empty map (or whatever it already was) — same as the original
// behavior.
func (s *Store) load() {
	info, statErr := os.Stat(s.path)
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var data map[string]map[string]Schedule
	if err := json.Unmarshal(b, &data); err != nil {
		return
	}
	s.data = data
	if statErr == nil {
		s.loadedAt = info.ModTime()
	}
}

// save writes the store to disk (caller holds s.mu and the file lock).
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, b, 0644); err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		s.loadedAt = info.ModTime()
	}
	return nil
}
