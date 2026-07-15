package memory

import (
	"path/filepath"
	"time"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWriteGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Write("/proj", "api-auth", "The refresh token flow works like this..."); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := s.Get("/proj", "api-auth")
	if err != nil || m == nil {
		t.Fatalf("get: %v m=%v", err, m)
	}
	if m.Content != "The refresh token flow works like this..." {
		t.Errorf("wrong body: %q", m.Content)
	}
}

func TestWriteUpsert(t *testing.T) {
	s := newTestStore(t)
	created, _ := s.Write("/proj", "slug", "body1")
	if !created {
		t.Errorf("first write should create")
	}
	created, _ = s.Write("/proj", "slug", "body2")
	if created {
		t.Errorf("second write should update, not create")
	}
	m, _ := s.Get("/proj", "slug")
	if m.Content != "body2" {
		t.Errorf("upsert did not update: %q", m.Content)
	}
}

func TestGetMissing(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Get("/proj", "nope")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil for missing memory")
	}
}

func TestSearchReturnsBodyAndPagination(t *testing.T) {
	s := newTestStore(t)
	s.Write("/proj", "db-choice", "SQLite gives us zero-dep local storage with FTS5")
	s.Write("/proj", "api-versioning", "We version via /api/v1 not headers")
	s.Write("/proj", "deploy", "Push to main triggers CI and deploy")

	res, err := s.Search("/proj", "SQLite", true, 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 1 || res.Returned != 1 {
		t.Fatalf("counts wrong: total=%d returned=%d", res.Total, res.Returned)
	}
	m := res.Results[0]
	if m.Slug != "db-choice" {
		t.Errorf("wrong result: %s", m.Slug)
	}
	// Search now returns the FULL body.
	if m.Content == "" {
		t.Errorf("search must return the body")
	}
	// Score is exposed (higher = more relevant).
	if m.Score == 0 {
		t.Errorf("score should be non-zero for a match")
	}
	// Dates present.
	if m.CreatedAt == 0 || m.UpdatedAt == 0 {
		t.Errorf("dates missing: %+v", m)
	}
}

func TestSearchPagination(t *testing.T) {
	s := newTestStore(t)
	for _, slug := range []string{"a", "b", "c", "d", "e"} {
		s.Write("/proj", slug, "common keyword content here")
	}
	// Page 1: skip 0, limit 2.
	p1, _ := s.Search("/proj", "keyword", true, 0, 2)
	if p1.Total != 5 || p1.Returned != 2 || p1.Skip != 0 || p1.Limit != 2 {
		t.Errorf("page1 meta wrong: %+v", p1)
	}
	// Page 2: skip 2, limit 2.
	p2, _ := s.Search("/proj", "keyword", true, 2, 2)
	if p2.Returned != 2 || p2.Skip != 2 {
		t.Errorf("page2 meta wrong: %+v", p2)
	}
	// Pages must not overlap.
	if p1.Results[0].Slug == p2.Results[0].Slug {
		t.Errorf("pagination overlap: %s", p1.Results[0].Slug)
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 15; i++ {
		s.Write("/proj", string(rune('a'+i)), "shared term")
	}
	res, _ := s.Search("/proj", "shared", true, 0, 0) // 0 → default 10
	if res.Limit != 10 || res.Returned != 10 || res.Total != 15 {
		t.Errorf("default limit wrong: %+v", res)
	}
}

func TestSearchPartitionedByCWD(t *testing.T) {
	s := newTestStore(t)
	s.Write("/projA", "shared-slug", "content about kubernetes for A")
	s.Write("/projB", "shared-slug", "content about kubernetes for B")

	res, _ := s.Search("/projA", "kubernetes", true, 0, 10)
	if res.Total != 1 || res.Results[0].Content != "content about kubernetes for A" {
		t.Errorf("cwd partition leaked: %+v", res)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	s.Write("/proj", "temp", "body text")
	ok, err := s.Delete("/proj", "temp")
	if err != nil || !ok {
		t.Fatalf("delete: %v ok=%v", err, ok)
	}
	if m, _ := s.Get("/proj", "temp"); m != nil {
		t.Errorf("memory still present after delete")
	}
	if ok, _ := s.Delete("/proj", "temp"); ok {
		t.Errorf("deleting missing should return false")
	}
	// FTS index cleaned — search finds nothing.
	if res, _ := s.Search("/proj", "body", true, 0, 10); res.Total != 0 {
		t.Errorf("fts index not cleaned after delete: %+v", res)
	}
}

func TestSearchRanking(t *testing.T) {
	s := newTestStore(t)
	s.Write("/proj", "exact", "how to run database migrations safely, migration steps")
	s.Write("/proj", "tangent", "we mentioned migration once here")
	res, _ := s.Search("/proj", "migration", true, 0, 10)
	if res.Total < 2 {
		t.Fatalf("expected 2 results, got %d", res.Total)
	}
	// More relevant memory ranks first, and has a higher score.
	if res.Results[0].Slug != "exact" {
		t.Errorf("BM25 ranking wrong, first=%s", res.Results[0].Slug)
	}
	if res.Results[0].Score < res.Results[1].Score {
		t.Errorf("scores not descending: %.2f < %.2f", res.Results[0].Score, res.Results[1].Score)
	}
}

func TestListMode(t *testing.T) {
	s := newTestStore(t)
	// Small gaps so updated_at differs and the ordering is deterministic.
	s.Write("/proj", "first", "content one")
	time.Sleep(2 * time.Millisecond)
	s.Write("/proj", "second", "content two")
	time.Sleep(2 * time.Millisecond)
	s.Write("/proj", "third", "content three")

	// No query → list all, most-recently-updated first.
	res, err := s.Search("/proj", "", true, 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 3 || res.Returned != 3 {
		t.Fatalf("list counts wrong: %+v", res)
	}
	// Most recent first (third was written last).
	if res.Results[0].Slug != "third" {
		t.Errorf("list order wrong, first=%s (want third)", res.Results[0].Slug)
	}
	// List mode carries no score.
	if res.Results[0].Score != 0 {
		t.Errorf("list mode should not set score: %v", res.Results[0].Score)
	}
}

func TestIncludeContentFalse(t *testing.T) {
	s := newTestStore(t)
	s.Write("/proj", "a", "some searchable content here")

	// Search with include_content=false → slug + dates, no content.
	res, _ := s.Search("/proj", "searchable", false, 0, 10)
	if res.Returned != 1 {
		t.Fatalf("expected 1 result, got %d", res.Returned)
	}
	if res.Results[0].Content != "" {
		t.Errorf("include_content=false must omit content, got %q", res.Results[0].Content)
	}
	if res.Results[0].Slug != "a" {
		t.Errorf("slug still expected: %+v", res.Results[0])
	}
	// List mode + no content.
	lst, _ := s.Search("/proj", "", false, 0, 10)
	if lst.Results[0].Content != "" {
		t.Errorf("list without content should omit content: %q", lst.Results[0].Content)
	}
}
