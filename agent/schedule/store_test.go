package schedule

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	s, err := Open(filepath.Join(t.TempDir(), "sched.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreUpsertAndList(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set("standup", "0 9 * * 1-5", "Generate standup"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("cleanup", "@daily", "Clean logs"); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
	// Sorted by slug: cleanup, standup.
	if list[0].Slug != "cleanup" || list[1].Slug != "standup" {
		t.Errorf("wrong order: %v", list)
	}

	// Edit preserves audit.
	s.RecordRun("standup", time.Now().UnixMilli())
	s.Set("standup", "0 10 * * 1-5", "Updated") // edit
	for _, sc := range s.List() {
		if sc.Slug == "standup" {
			if sc.Runs != 1 {
				t.Errorf("edit should preserve runs, got %d", sc.Runs)
			}
			if sc.Cron != "0 10 * * 1-5" || sc.Prompt != "Updated" {
				t.Errorf("edit didn't apply: %+v", sc)
			}
		}
	}
}

func TestStoreInvalidCron(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set("bad", "not a cron", "x"); err == nil {
		t.Error("invalid cron should error")
	}
	if err := s.Set("bad", "99 99 * * *", "x"); err == nil {
		t.Error("out-of-range cron should error")
	}
}

func TestStoreDelete(t *testing.T) {
	s := newTestStore(t)
	s.Set("x", "@hourly", "p")
	ok, _ := s.Delete("x")
	if !ok {
		t.Error("delete should report existed")
	}
	ok, _ = s.Delete("x")
	if ok {
		t.Error("second delete should report not existed")
	}
}

func TestStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	s1, _ := Open(path)
	s1.Set("x", "@daily", "prompt")
	// Reopen and verify it loaded.
	s2, _ := Open(path)
	if len(s2.List()) != 1 {
		t.Error("schedule should persist across reopen")
	}
}

func TestEngineStandardCronFires(t *testing.T) {
	s := newTestStore(t)
	s.Set("minutely", "* * * * *", "run") // every minute
	var fired []string
	eng := NewEngine(s, func(slug, prompt string) { fired = append(fired, slug) })

	// Engine started at 09:00:30, never run. By 09:01:05 the 09:01:00 tick has
	// passed → fires once.
	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)
	eng.evaluate(start, now)
	if len(fired) != 1 {
		t.Fatalf("expected 1 fire, got %d", len(fired))
	}
	s.RecordRun("minutely", now.UnixMilli()) // engine does this on fire

	// Same minute again → anchored on last run (09:01:05), next is 09:02:00, not
	// yet at 09:01:40 → no double fire.
	fired = nil
	eng.evaluate(start, time.Date(2026, 1, 1, 9, 1, 40, 0, time.Local))
	if len(fired) != 0 {
		t.Errorf("should not double-fire within the minute, got %d", len(fired))
	}
}

// The regression: @every is a relative schedule. Anchoring on last run (not a
// moving cursor) is what makes it fire.
func TestEngineEveryFires(t *testing.T) {
	s := newTestStore(t)
	s.Set("tick", "@every 1m", "run")
	var fired []string
	eng := NewEngine(s, func(slug, prompt string) { fired = append(fired, slug) })

	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.Local)
	// Before 1m elapsed → not due.
	eng.evaluate(start, time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local))
	if len(fired) != 0 {
		t.Fatalf("@every 1m should not fire at +30s, got %d", len(fired))
	}
	// After 1m → fires.
	eng.evaluate(start, time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local))
	if len(fired) != 1 {
		t.Fatalf("@every 1m should fire after 1m, got %d", len(fired))
	}
}

func TestEngineReadsFreshEachEval(t *testing.T) {
	s := newTestStore(t)
	var fired []string
	eng := NewEngine(s, func(slug, prompt string) { fired = append(fired, slug) })

	start := time.Date(2026, 1, 1, 9, 0, 30, 0, time.Local)
	now := time.Date(2026, 1, 1, 9, 1, 5, 0, time.Local)

	// No schedules yet → nothing fires.
	eng.evaluate(start, now)
	if len(fired) != 0 {
		t.Fatalf("no schedules → no fire, got %d", len(fired))
	}

	// Add one WITHOUT restarting the engine → next evaluate picks it up.
	s.Set("added-live", "* * * * *", "run")
	eng.evaluate(start, now)
	if len(fired) != 1 || fired[0] != "added-live" {
		t.Errorf("a schedule added at runtime should fire, got %v", fired)
	}

	// Delete it → stops firing.
	fired = nil
	s.Delete("added-live")
	eng.evaluate(start, now)
	if len(fired) != 0 {
		t.Errorf("a deleted schedule should stop firing, got %v", fired)
	}
}

func TestEngineStartStop(t *testing.T) {
	s := newTestStore(t)
	eng := NewEngine(s, func(slug, prompt string) {})
	ctx := context.Background()
	eng.Start(ctx)
	eng.Stop() // must not panic / deadlock
}

func TestValidateMinInterval(t *testing.T) {
	// Sub-minute @every → rejected.
	for _, spec := range []string{"@every 30s", "@every 59s", "@every 500ms"} {
		if err := ValidateCron(spec); err == nil {
			t.Errorf("%q should be rejected (sub-minute)", spec)
		}
	}
	// 1 minute and above → OK.
	for _, spec := range []string{"@every 1m", "@every 90s", "@every 1h", "@hourly", "* * * * *", "0 9 * * 1-5"} {
		if err := ValidateCron(spec); err != nil {
			t.Errorf("%q should be valid: %v", spec, err)
		}
	}
}
