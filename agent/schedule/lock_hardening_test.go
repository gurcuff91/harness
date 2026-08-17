package schedule

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These mirror internal/config/update_credential_test.go's shape — the same
// guarantees UpdateCredential provides, applied to schedules. See
// docs/plans/2026-08-17-schedule-store-lock-hardening-design.md.

func TestUpdateSchedule_MissingIsReportedNotWritten(t *testing.T) {
	s := newTestStore(t)
	var sawOK bool
	err := s.UpdateSchedule("owner", "slug", func(cur Schedule, ok bool) (Schedule, UpdateAction, error) {
		sawOK = ok
		return Schedule{}, ActionNoop, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawOK {
		t.Error("ok should be false for a schedule that doesn't exist")
	}
	if len(s.List()) != 0 {
		t.Error("nothing should have been persisted")
	}
}

func TestUpdateSchedule_ActionFlagControlsPersistence(t *testing.T) {
	s := newTestStore(t)
	sched := Schedule{Cron: "@daily", Prompt: "hi"}

	// ActionNoop: must not persist.
	if err := s.UpdateSchedule("owner", "slug", func(Schedule, bool) (Schedule, UpdateAction, error) {
		return sched, ActionNoop, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("ActionNoop must not persist")
	}

	// ActionWrite: must persist.
	if err := s.UpdateSchedule("owner", "slug", func(Schedule, bool) (Schedule, UpdateAction, error) {
		return sched, ActionWrite, nil
	}); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 1 || list[0].Prompt != "hi" {
		t.Fatalf("ActionWrite should have persisted, got %+v", list)
	}
}

func TestUpdateSchedule_FnErrorPropagatesAndSkipsWrite(t *testing.T) {
	s := newTestStore(t)
	sentinel := errors.New("boom")
	err := s.UpdateSchedule("owner", "slug", func(Schedule, bool) (Schedule, UpdateAction, error) {
		return Schedule{Cron: "@daily", Prompt: "x"}, ActionWrite, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel error, got %v", err)
	}
	if len(s.List()) != 0 {
		t.Error("an fn error must prevent the write")
	}
}

// The core regression: a second Store instance (simulating a second process)
// must see the freshest on-disk state a first one just wrote — the guard that
// prevents a lost write / clobbered audit trail.
func TestUpdateSchedule_SeesFreshestDiskStateAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := writer.Set("standup", "@daily", "generate standup", "sess-A"); err != nil {
		t.Fatal(err)
	}

	var seenPrompt string
	err = reader.UpdateSchedule("sess-A", "standup", func(cur Schedule, ok bool) (Schedule, UpdateAction, error) {
		if !ok {
			t.Fatal("reader did not see the schedule the writer just persisted")
		}
		seenPrompt = cur.Prompt
		return cur, ActionNoop, nil // just observing
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenPrompt != "generate standup" {
		t.Fatalf("saw stale prompt %q, want the freshly-written one", seenPrompt)
	}
}

// The exact bug this hardening fixes: RecordRun (what the Engine calls every
// tick) must apply its increment ON TOP of a concurrent edit another process
// just made — never clobber the whole file with a stale in-memory copy.
func TestRecordRun_DoesNotClobberConcurrentEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	engineStore, err := Open(path) // simulates the --scheduler process
	if err != nil {
		t.Fatal(err)
	}
	userStore, err := Open(path) // simulates a different process's session
	if err != nil {
		t.Fatal(err)
	}

	if err := engineStore.Set("standup", "@daily", "v1 prompt", "sess-A"); err != nil {
		t.Fatal(err)
	}
	// The user's process edits the SAME schedule's prompt from elsewhere,
	// persisting a change engineStore hasn't seen yet in memory.
	if err := userStore.Set("standup", "@daily", "v2 prompt EDITED", "sess-A"); err != nil {
		t.Fatal(err)
	}

	// The engine now records a run — it must pick up v2 (freshest disk state),
	// not silently resurrect/overwrite with whatever it had cached from before
	// the edit.
	if err := engineStore.RecordRun("standup", "sess-A", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	list := userStore.List()
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 schedule, got %d", len(list))
	}
	if list[0].Prompt != "v2 prompt EDITED" {
		t.Errorf("prompt = %q, want the edit to survive: %q", list[0].Prompt, "v2 prompt EDITED")
	}
	if list[0].Runs != 1 {
		t.Errorf("Runs = %d, want 1 (RecordRun's increment must still apply)", list[0].Runs)
	}
}

// RecordRun against a schedule deleted by another process must be a no-op —
// never resurrect a deleted schedule just to record a run against it.
func TestRecordRun_NoopsIfDeletedConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	engineStore, _ := Open(path)
	userStore, _ := Open(path)

	if err := engineStore.Set("standup", "@daily", "hi", "sess-A"); err != nil {
		t.Fatal(err)
	}
	if ok, err := userStore.Delete("standup", "sess-A"); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}

	if err := engineStore.RecordRun("standup", "sess-A", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if list := userStore.List(); len(list) != 0 {
		t.Fatalf("RecordRun must not resurrect a deleted schedule, got %+v", list)
	}
}

// No deadlock, no re-entrant lock: UpdateSchedule owns the file lock
// internally and there is no way for fn to ask for it again. This also
// stresses that concurrent UpdateSchedule calls serialize instead of
// corrupting each other.
func TestUpdateSchedule_ConcurrentCallsSerializeCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	var inside atomic.Int32
	var wg sync.WaitGroup

	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Open(path) // separate instance per goroutine, like separate processes
			if err != nil {
				t.Errorf("goroutine %d: open: %v", i, err)
				return
			}
			err = s.UpdateSchedule("sess-A", "slug", func(cur Schedule, ok bool) (Schedule, UpdateAction, error) {
				n := inside.Add(1)
				defer inside.Add(-1)
				if n > 1 {
					t.Errorf("two callers inside UpdateSchedule's critical section at once: %d", n)
				}
				time.Sleep(time.Millisecond)
				return Schedule{Cron: "@daily", Prompt: "x"}, ActionWrite, nil
			})
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

// Delete racing a concurrent Set/RecordRun on the same key must resolve
// deterministically — no corruption, no partial state, whichever the lock
// serializes second simply wins.
func TestDelete_RacingSetResolvesDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	a, _ := Open(path)
	b, _ := Open(path)

	if err := a.Set("slug", "@daily", "hi", "sess-A"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = a.Delete("slug", "sess-A") }()
	go func() { defer wg.Done(); _ = b.Set("slug", "@daily", "hi again", "sess-A") }()
	wg.Wait()

	// Either outcome (deleted, or present with the new value) is valid — the
	// invariant is no corruption: List() must decode cleanly and show AT MOST
	// one entry for this key, never a torn/duplicated write.
	list := a.List()
	count := 0
	for _, sc := range list {
		if sc.Slug == "slug" && sc.Owner == "sess-A" {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("expected at most 1 entry for the racing key, got %d: %+v", count, list)
	}
}
