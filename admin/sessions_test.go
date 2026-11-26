package admin_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/store"
)

func TestSessionsListGetMessages(t *testing.T) {
	st := store.NewInMemoryStore()
	seedSession(t, st, "s1", "one", 3)
	ad := newAdmin(t, admin.WithSessionStore(st))

	all, err := ad.Sessions.List("")
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %v", all, err)
	}
	none, err := ad.Sessions.List("/other")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("a cwd with nothing must be an empty non-nil slice: %#v %v", none, err)
	}
	if m, err := ad.Sessions.Get("s1"); err != nil || m.Name != "one" {
		t.Fatalf("get: %v %v", m, err)
	}
	if _, err := ad.Sessions.Get("nope"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	msgs, from, total, err := ad.Sessions.Messages("s1", store.MessageWindow{From: 1})
	if err != nil || len(msgs) != 2 || from != 1 || total != 3 {
		t.Fatalf("window: %d %d %d %v", len(msgs), from, total, err)
	}
	if _, _, _, err := ad.Sessions.Messages("nope", store.MessageWindow{}); !errors.Is(err, admin.ErrNotFound) || !strings.Contains(err.Error(), "session nope not found") {
		t.Fatalf("messages of unknown: %v", err)
	}
}

func TestSessionsDeleteRenameRefuseActive(t *testing.T) {
	st := store.NewInMemoryStore()
	seedSession(t, st, "live", "live", 1)
	seedSession(t, st, "cold", "cold", 1)
	ad := newAdmin(t, admin.WithSessionStore(st), admin.WithActiveSessions(func() []string { return []string{"live"} }))

	if err := ad.Sessions.Delete("live"); !errors.Is(err, admin.ErrSessionActive) {
		t.Fatalf("delete active: %v", err)
	}
	if err := ad.Sessions.Rename("live", "x"); !errors.Is(err, admin.ErrSessionActive) {
		t.Fatalf("rename active: %v", err)
	}
	if m, _ := ad.Sessions.Get("live"); m.Name != "live" {
		t.Fatal("a refused rename must change nothing")
	}
	if err := ad.Sessions.Rename("cold", "renamed"); err != nil {
		t.Fatal(err)
	}
	if m, _ := ad.Sessions.Get("cold"); m.Name != "renamed" {
		t.Fatalf("rename: %q", m.Name)
	}
	if err := ad.Sessions.Rename("ghost", "x"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("rename absent: %v", err)
	}
	if err := ad.Sessions.Delete("cold"); err != nil {
		t.Fatal(err)
	}
	if err := ad.Sessions.Delete("cold"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := ad.Sessions.Get("live"); err != nil {
		t.Fatal("the active session must survive")
	}
}

func TestSessionsWithoutActiveFuncChecksNothing(t *testing.T) {
	st := store.NewInMemoryStore()
	seedSession(t, st, "a", "a", 0)
	ad := newAdmin(t, admin.WithSessionStore(st))
	if err := ad.Sessions.Delete("a"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsResetCountAndProfiles(t *testing.T) {
	st := store.NewInMemoryStore()
	m := seedSession(t, st, "r", "r", 0)
	m.ResetCount, m.Profiles = 3, []string{"slack"}
	st.SaveMeta(m)
	ad := newAdmin(t, admin.WithSessionStore(st))
	if ad.Sessions.ResetCount("r") != 3 || ad.Sessions.ResetCount("x") != 0 {
		t.Fatal("reset count")
	}
	if p := ad.Sessions.Profiles("r"); len(p) != 1 || p[0] != "slack" || ad.Sessions.Profiles("x") != nil {
		t.Fatalf("profiles %v", p)
	}
}

func TestExportFormat(t *testing.T) {
	isolate(t)
	st := store.NewInMemoryStore()
	meta := seedSession(t, st, "11111111-aaaa", "My Session!", 4)
	ad := admin.New(admin.WithSessionStore(st))
	t.Cleanup(func() { ad.Close() })

	var buf bytes.Buffer
	n, err := ad.Sessions.Export(meta.ID, &buf)
	if err != nil || n != 4 {
		t.Fatalf("export: n=%d err=%v", n, err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d lines, want header + 4", len(lines))
	}
	var h admin.ExportHeader
	if err := json.Unmarshal([]byte(lines[0]), &h); err != nil {
		t.Fatal(err)
	}
	if h.Format != "harness-session-export" || h.Version != 1 || h.Harness == "" || h.Meta.ID != meta.ID || h.Meta.Name != "My Session!" {
		t.Fatalf("header = %+v", h)
	}
	if admin.ExportFileName(meta) != "my-session-11111111.export.jsonl" {
		t.Fatalf("file name %q", admin.ExportFileName(meta))
	}
	// ExportAs carries the caller's (live) meta, not the stored one.
	live := meta
	live.Model = "live/model"
	var buf2 bytes.Buffer
	if _, err := ad.Sessions.ExportAs(live, &buf2); err != nil {
		t.Fatal(err)
	}
	var h2 admin.ExportHeader
	_ = json.Unmarshal([]byte(strings.SplitN(buf2.String(), "\n", 2)[0]), &h2)
	if h2.Meta.Model != "live/model" {
		t.Fatalf("ExportAs meta model = %q", h2.Meta.Model)
	}
	if _, err := ad.Sessions.Export("ghost", &buf); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("export absent: %v", err)
	}
}

func TestExportFileName(t *testing.T) {
	for in, want := range map[string]string{
		"Refactor del Parser!":         "refactor-del-parser-5111a1b3.export.jsonl",
		"":                             "session-5111a1b3.export.jsonl",
		"🚀 ¿qué?":                      "qu-5111a1b3.export.jsonl",
		"🚀 ¿?":                         "session-5111a1b3.export.jsonl",
		"a/b\\c  d":                    "a-b-c-d-5111a1b3.export.jsonl",
		strings.Repeat("x", 80):        strings.Repeat("x", 50) + "-5111a1b3.export.jsonl",
		strings.Repeat("y", 49) + " z": strings.Repeat("y", 49) + "-5111a1b3.export.jsonl", // a cut never ends in "-"
	} {
		if got := admin.ExportFileName(store.SessionMeta{ID: "5111a1b3-bb6d-45d1", Name: in}); got != want {
			t.Errorf("ExportFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultSessionStoreIsOpenedAndClosedByAdmin(t *testing.T) {
	isolate(t)
	ad := admin.New()
	if l, err := ad.Sessions.List(""); err != nil || len(l) != 0 {
		t.Fatalf("default store: %v %v", l, err)
	}
	if err := ad.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ad.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
}

func TestGivenSessionStoreIsNotClosedByAdmin(t *testing.T) {
	st := &closeCounter{SessionStore: store.NewInMemoryStore()}
	ad := admin.New(admin.WithSessionStore(st))
	ad.Sessions.List("")
	ad.Close()
	if st.n != 0 {
		t.Fatal("admin closed a store it was given")
	}
}

type closeCounter struct {
	store.SessionStore
	n int
}

func (c *closeCounter) Close() error { c.n++; return nil }
