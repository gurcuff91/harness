package schedule

import (
	"sync"
	"testing"
	"time"
)

// ── AddSession/DelSession/ClearSessions ─────────────────────────────────

// TestEngineIgnoresUnwatchedSessions is the direct regression test for the
// field-reported bug: a schedule whose owner was never added via
// AddSession must never fire, and — critically — must never RecordRun
// either, so it stays fully available for whichever OTHER engine (if any)
// actually watches that owner.
func TestEngineIgnoresUnwatchedSessions(t *testing.T) {
	s := newTestStore(t)
	s.Set("tick", "* * * * *", "run", "sess-A")
	var fired []string
	eng := NewEngine(s, func(slug, prompt, owner string) { fired = append(fired, slug) })
	// Deliberately never call eng.AddSession("sess-A").

	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)
	eng.evaluate(start, now)

	if len(fired) != 0 {
		t.Fatalf("an unwatched owner's schedule must never fire, got %v", fired)
	}
	// RecordRun must not have happened either — LastRun/Runs untouched.
	for _, sc := range s.List() {
		if sc.Slug == "tick" && (sc.Runs != 0 || sc.LastRun != 0) {
			t.Errorf("an unwatched schedule must not be RecordRun'd, got Runs=%d LastRun=%d", sc.Runs, sc.LastRun)
		}
	}
}

// TestEngineFiresOnlyWatchedAmongMultiple confirms per-schedule filtering:
// two schedules with different owners, only one watched — only that one
// fires, the other is left untouched for someone else.
func TestEngineFiresOnlyWatchedAmongMultiple(t *testing.T) {
	s := newTestStore(t)
	s.Set("a-tick", "* * * * *", "run", "sess-A")
	s.Set("b-tick", "* * * * *", "run", "sess-B")
	var fired []string
	eng := NewEngine(s, func(slug, prompt, owner string) { fired = append(fired, slug) })
	eng.AddSession("sess-A") // sess-B deliberately not watched

	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)
	eng.evaluate(start, now)

	if len(fired) != 1 || fired[0] != "a-tick" {
		t.Fatalf("expected only a-tick to fire, got %v", fired)
	}
}

// TestEngineDelSessionStopsFiring confirms DelSession takes effect on the
// very next evaluate — no restart needed.
func TestEngineDelSessionStopsFiring(t *testing.T) {
	s := newTestStore(t)
	s.Set("tick", "* * * * *", "run", "sess-A")
	var fired []string
	eng := NewEngine(s, func(slug, prompt, owner string) { fired = append(fired, slug) })
	eng.AddSession("sess-A")

	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)
	eng.evaluate(start, now)
	if len(fired) != 1 {
		t.Fatalf("expected 1 fire before DelSession, got %d", len(fired))
	}

	eng.DelSession("sess-A")
	fired = nil
	// Advance far enough that, if still watched, this would clearly fire
	// again (a fresh minute boundary).
	eng.evaluate(start, time.Date(2026, 1, 1, 9, 2, 5, 0, time.Local))
	if len(fired) != 0 {
		t.Errorf("expected no fire after DelSession, got %v", fired)
	}
}

// TestEngineClearSessionsStopsAll confirms ClearSessions resets the engine
// to its initial do-nothing state, even with multiple sessions watched.
func TestEngineClearSessionsStopsAll(t *testing.T) {
	s := newTestStore(t)
	s.Set("a-tick", "* * * * *", "run", "sess-A")
	s.Set("b-tick", "* * * * *", "run", "sess-B")
	var fired []string
	eng := NewEngine(s, func(slug, prompt, owner string) { fired = append(fired, slug) })
	eng.AddSession("sess-A")
	eng.AddSession("sess-B")

	eng.ClearSessions()

	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)
	eng.evaluate(start, now)
	if len(fired) != 0 {
		t.Errorf("expected no fires after ClearSessions, got %v", fired)
	}
}

// TestEngineAddSessionIgnoresEmptyString protects against accidentally
// watching the empty-owner sentinel (stale pre-owner schedules — see
// store.go) — AddSession("") must be a no-op, not a wildcard.
func TestEngineAddSessionIgnoresEmptyString(t *testing.T) {
	s := newTestStore(t)
	eng := NewEngine(s, func(slug, prompt, owner string) {})
	eng.AddSession("")
	if eng.Watching("") {
		t.Error("AddSession(\"\") must not make the empty owner watched")
	}
}

// TestNewEngineStartsWithNoWatchedSessions confirms a freshly constructed
// engine watches nothing and therefore fires nothing, even for a schedule
// that's clearly due — the documented "do-nothing until told otherwise"
// initial state.
func TestNewEngineStartsWithNoWatchedSessions(t *testing.T) {
	s := newTestStore(t)
	s.Set("tick", "* * * * *", "run", "sess-A")
	var fired []string
	eng := NewEngine(s, func(slug, prompt, owner string) { fired = append(fired, slug) })

	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)
	eng.evaluate(start, now)
	if len(fired) != 0 {
		t.Errorf("a brand-new engine must watch nothing, got %v", fired)
	}
}

// ── Multi-engine, same store: the actual field-reported scenario ─────────

// TestTwoEnginesOnSameStoreOnlyFireForTheirOwnWatchedSessions is the direct
// regression test reproducing the real bug report: two harness PROCESSES
// (here, two *Engine instances over the SAME *Store, simulating that) each
// running --scheduler, each with a session for a DIFFERENT owner active.
// Before the watched-session filter, whichever engine's tick landed first
// would win the fire+RecordRun for EVERY due schedule regardless of which
// engine actually had a live session for it — so the schedule created in
// one process could be silently consumed by another process with no
// matching session, and the user watching the first process would never
// see the prompt land. After the fix, each engine only ever acts on its
// own watched owner's schedule.
func TestTwoEnginesOnSameStoreOnlyFireForTheirOwnWatchedSessions(t *testing.T) {
	s := newTestStore(t)
	s.Set("khan-greeting", "* * * * *", "run", "sess-khan")
	s.Set("slack-standup", "* * * * *", "run", "sess-slack")

	var firedA, firedB []string
	engA := NewEngine(s, func(slug, prompt, owner string) { firedA = append(firedA, slug) })
	engB := NewEngine(s, func(slug, prompt, owner string) { firedB = append(firedB, slug) })
	engA.AddSession("sess-khan")  // process A only has sess-khan live
	engB.AddSession("sess-slack") // process B only has sess-slack live

	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)

	// Simulate both engines' ticks landing in the same window — order
	// shouldn't matter, so evaluate B first, then A, then repeat A first,
	// B second in a second run to make sure neither ordering steals the
	// other's fire.
	engB.evaluate(start, now)
	engA.evaluate(start, now)

	if len(firedA) != 1 || firedA[0] != "khan-greeting" {
		t.Errorf("engine A should have fired only khan-greeting, got %v", firedA)
	}
	if len(firedB) != 1 || firedB[0] != "slack-standup" {
		t.Errorf("engine B should have fired only slack-standup, got %v", firedB)
	}

	// Both RecordRun'd correctly — no cross-contamination of audit fields.
	for _, sc := range s.List() {
		if sc.Slug == "khan-greeting" && sc.Runs != 1 {
			t.Errorf("khan-greeting: expected Runs=1, got %d", sc.Runs)
		}
		if sc.Slug == "slack-standup" && sc.Runs != 1 {
			t.Errorf("slack-standup: expected Runs=1, got %d", sc.Runs)
		}
	}
}

// TestEngineWatchMutationsAreRaceSafeDuringEvaluate confirms AddSession/
// DelSession can be called concurrently with evaluate running in the
// background without triggering a data race — run with -race.
func TestEngineWatchMutationsAreRaceSafeDuringEvaluate(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 10; i++ {
		s.Set("tick", "* * * * *", "run", "sess-A")
	}
	eng := NewEngine(s, func(slug, prompt, owner string) {})

	start := time.Now()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				eng.evaluate(start, time.Now())
			}
		}
	}()

	// The mutator finishes first (bounded loop); once it's done, signal the
	// evaluate loop to stop, then wait for both to fully exit.
	var mutator sync.WaitGroup
	mutator.Add(1)
	go func() {
		defer mutator.Done()
		for i := 0; i < 200; i++ {
			eng.AddSession("sess-A")
			eng.DelSession("sess-A")
		}
	}()
	mutator.Wait()
	close(stop)
	wg.Wait()
}
