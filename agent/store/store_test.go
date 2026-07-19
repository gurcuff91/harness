package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

func newMeta(id, cwd string) SessionMeta {
	return SessionMeta{ID: id, CWD: cwd, Model: "anthropic/x", Thinking: "off", CreatedAt: time.Now()}
}

// ports returns both SessionStore implementations so each test runs against both.
func ports(t *testing.T) map[string]SessionStore {
	t.Helper()
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	return map[string]SessionStore{
		"memory": NewInMemoryStore(),
		"file":   fs,
	}
}

func TestPortMetaRoundTrip(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			meta := newMeta("s1", "/proj")
			if err := p.SaveMeta(meta); err != nil {
				t.Fatal(err)
			}
			got, found, err := p.LoadMeta("s1")
			if err != nil || !found {
				t.Fatalf("load: found=%v err=%v", found, err)
			}
			if got.ID != "s1" || got.CWD != "/proj" {
				t.Errorf("wrong meta: %+v", got)
			}
			// Missing session → found=false, no error.
			if _, found, err := p.LoadMeta("nope"); found || err != nil {
				t.Errorf("missing: found=%v err=%v", found, err)
			}
		})
	}
}

func TestPortListMetas(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			p.SaveMeta(newMeta("a", "/one"))
			p.SaveMeta(newMeta("b", "/one"))
			p.SaveMeta(newMeta("c", "/two"))

			if got, _ := p.ListMetas("/one"); len(got) != 2 {
				t.Errorf("cwd /one: want 2, got %d", len(got))
			}
			if got, _ := p.ListMetas("/two"); len(got) != 1 {
				t.Errorf("cwd /two: want 1, got %d", len(got))
			}
			if got, _ := p.ListMetas(""); len(got) != 3 { // "" → all
				t.Errorf("all: want 3, got %d", len(got))
			}
		})
	}
}

func TestPortAppendAndLoadMessages(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			p.SaveMeta(newMeta("s", "/p"))
			for i := 0; i < 5; i++ {
				if err := p.AppendMessage("s", types.NewUserTextMessage("m")); err != nil {
					t.Fatal(err)
				}
			}
			all, _ := p.LoadMessages("s", 0)
			if len(all) != 5 {
				t.Fatalf("from 0: want 5, got %d", len(all))
			}
			// fromIndex slices the log absolutely.
			tail, _ := p.LoadMessages("s", 3)
			if len(tail) != 2 {
				t.Errorf("from 3: want 2, got %d", len(tail))
			}
			// Out-of-range fromIndex is tolerated (treated as 0 or empty tail).
			if got, _ := p.LoadMessages("s", 99); len(got) != 0 {
				t.Errorf("from 99: want 0, got %d", len(got))
			}
		})
	}
}

func TestPortDeleteSession(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			p.SaveMeta(newMeta("s", "/p"))
			p.AppendMessage("s", types.NewUserTextMessage("m"))
			if err := p.DeleteSession("s"); err != nil {
				t.Fatal(err)
			}
			if _, found, _ := p.LoadMeta("s"); found {
				t.Error("session should be gone")
			}
		})
	}
}

// ── the *Session handle (domain logic over the port) ──────────────────────

func TestSessionHandleBasics(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			s, err := CreateSession(p, newMeta("s", "/p"))
			if err != nil {
				t.Fatal(err)
			}
			s.AddMessage(types.NewUserTextMessage("hello"))
			s.AddMessage(types.NewUserTextMessage("world"))
			if got := s.Messages(); len(got) != 2 {
				t.Fatalf("working set: want 2, got %d", len(got))
			}
			// Durable: a fresh open sees the same messages.
			s2, _ := OpenSession(p, "s")
			if got := s2.Messages(); len(got) != 2 {
				t.Errorf("reopened working set: want 2, got %d", len(got))
			}
		})
	}
}

func TestSessionHandleCompaction(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			s, _ := CreateSession(p, newMeta("s", "/p"))
			s.AddMessage(types.NewUserTextMessage("a"))
			s.AddMessage(types.NewUserTextMessage("b"))
			s.AddMessage(types.NewUserTextMessage("c"))

			if err := s.AddCompactionSummary("summary"); err != nil {
				t.Fatal(err)
			}
			// Working set is now just the checkpoint.
			if got := s.Messages(); len(got) != 1 {
				t.Fatalf("post-compact working: want 1 (checkpoint), got %d", len(got))
			}
			// Full history is preserved: 3 originals + 1 checkpoint.
			if got := s.AllMessages(); len(got) != 4 {
				t.Errorf("all messages: want 4, got %d", len(got))
			}
			// Add more after the checkpoint.
			s.AddMessage(types.NewUserTextMessage("d"))
			if got := s.Messages(); len(got) != 2 { // checkpoint + d
				t.Errorf("working after add: want 2, got %d", len(got))
			}

			// Resume restores only the working set (checkpoint + d), but full
			// history stays available via AllMessages.
			s2, _ := OpenSession(p, "s")
			if got := s2.Messages(); len(got) != 2 {
				t.Errorf("resumed working: want 2, got %d", len(got))
			}
			if got := s2.AllMessages(); len(got) != 5 {
				t.Errorf("resumed all: want 5, got %d", len(got))
			}
			if m := s2.Meta(); m.CompactCount != 1 {
				t.Errorf("compact count: want 1, got %d", m.CompactCount)
			}
		})
	}
}

func TestSessionHandleUpdateMeta(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			s, _ := CreateSession(p, newMeta("s", "/p"))
			m := s.Meta()
			m.Model = "openai/gpt"
			if err := s.UpdateMeta(m); err != nil {
				t.Fatal(err)
			}
			s2, _ := OpenSession(p, "s")
			if s2.Meta().Model != "openai/gpt" {
				t.Errorf("meta not persisted: %s", s2.Meta().Model)
			}
		})
	}
}

func TestRename(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			CreateSession(p, newMeta("s", "/p"))
			if err := Rename(p, "s", "My Session"); err != nil {
				t.Fatal(err)
			}
			meta, _, _ := p.LoadMeta("s")
			if meta.Name != "My Session" {
				t.Errorf("rename failed: %q", meta.Name)
			}
			if err := Rename(p, "missing", "x"); err == nil {
				t.Error("rename of missing session should error")
			}
		})
	}
}

func TestOpenSessionNotFound(t *testing.T) {
	for name, p := range ports(t) {
		t.Run(name, func(t *testing.T) {
			s, err := OpenSession(p, "ghost")
			if err != nil || s != nil {
				t.Errorf("missing session: want (nil,nil), got (%v,%v)", s, err)
			}
		})
	}
}
