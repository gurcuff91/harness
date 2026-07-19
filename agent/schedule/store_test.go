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

func TestEngineFires(t *testing.T) {
	s := newTestStore(t)
	s.Set("tick", "@every 1s", "do it")
	fired := make(chan string, 1)
	eng := NewEngine(s, func(slug, prompt string) { fired <- slug })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	select {
	case slug := <-fired:
		if slug != "tick" {
			t.Errorf("wrong slug fired: %s", slug)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("schedule did not fire within 3s")
	}
}
