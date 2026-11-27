// Package storetest holds the behavior every memory.Store implementation must
// have — the bar SQLiteStore, the in-memory Reference and any third
// implementation (such as one served by a remote client) must pass.
//
// It requires only what is PORTABLE. Relevance is checked by ORDER, never by
// the value of Score (short memories score 0.00 on SQLite), and prefix matching
// (SQLite finds "deploy" with "dep") is neither required nor forbidden: every
// word searched for here is a whole word, and the memories that must not match
// share no prefix with it.
package storetest

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/memory"
)

// Store is memory.Store's method set (a compile-time test pins that the two
// stay equal), so a store that does not import this package can be driven by
// the suite.
type Store interface {
	Write(cwd, slug, content string, global bool) (created bool, err error)
	Get(cwd, slug string, global bool) (m memory.Memory, found bool, err error)
	Search(cwd, query string, includeContent bool, skip, limit int) (memory.SearchResult, error)
	Delete(cwd, slug string, global bool) (deleted bool, err error)
	Close() error
}

// TB is the part of testing.TB the cases use, so a case can be driven by a
// recording fake (to prove the suite catches a broken store).
type TB interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// Case is one behavior of the contract.
type Case struct {
	Name string
	Run  func(t TB, s Store)
}

// RunContract runs every case in Cases as a subtest, each on a fresh store
// from newStore (closed when the case ends).
func RunContract(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	for _, c := range Cases {
		t.Run(c.Name, func(t *testing.T) {
			s := newStore(t)
			defer s.Close()
			c.Run(t, s)
		})
	}
}

const (
	projA = "/work/a"
	projB = "/work/b"
)

// tick separates two writes by more than the millisecond resolution of
// updated_at, so the order of "most recently updated" is observable.
func tick() { time.Sleep(3 * time.Millisecond) }

func write(t TB, s Store, cwd, slug, content string, global bool) bool {
	t.Helper()
	created, err := s.Write(cwd, slug, content, global)
	if err != nil {
		t.Fatalf("Write(%q, %q, global=%v): %v", cwd, slug, global, err)
	}
	return created
}

func search(t TB, s Store, cwd, query string, content bool, skip, limit int) memory.SearchResult {
	t.Helper()
	r, err := s.Search(cwd, query, content, skip, limit)
	if err != nil {
		t.Fatalf("Search(%q, %q, skip=%d, limit=%d): %v", cwd, query, skip, limit, err)
	}
	return r
}

func slugs(r memory.SearchResult) []string {
	out := make([]string, 0, len(r.Results))
	for _, m := range r.Results {
		out = append(out, m.Slug)
	}
	return out
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// wantSet fails unless got holds exactly the slugs in want, in any order.
func wantSet(t TB, what string, got memory.SearchResult, want ...string) {
	t.Helper()
	if !slices.Equal(sorted(slugs(got)), sorted(want)) || got.Total != len(want) || got.Returned != len(want) {
		t.Errorf("%s = %v (total %d, returned %d); want exactly %v", what, slugs(got), got.Total, got.Returned, sorted(want))
	}
}

// Cases is the whole contract.
var Cases = []Case{
	{"WriteIsUpsertAndReportsCreated", func(t TB, s Store) {
		if !write(t, s, projA, "note", "one", false) {
			t.Error("first Write must report created=true")
		}
		if write(t, s, projA, "note", "two", false) {
			t.Error("rewriting the same cwd+slug must report created=false")
		}
		m, found, err := s.Get(projA, "note", false)
		if err != nil || !found || m.Content != "two" {
			t.Fatalf("Get after rewrite = %+v, %v, %v; want content \"two\"", m, found, err)
		}
		if r := search(t, s, projA, "", false, 0, 0); r.Total != 1 {
			t.Errorf("a rewrite must not add a memory: total = %d, want 1", r.Total)
		}
	}},
	{"GetMissingIsNotFoundNotError", func(t TB, s Store) {
		m, found, err := s.Get(projA, "nope", false)
		if err != nil || found || m != (memory.Memory{}) {
			t.Fatalf("Get(missing) = %+v, %v, %v; want zero, false, nil", m, found, err)
		}
		m, found, err = s.Get(projA, "nope", true)
		if err != nil || found || m != (memory.Memory{}) {
			t.Fatalf("Get(missing, global) = %+v, %v, %v; want zero, false, nil", m, found, err)
		}
	}},
	{"GetReturnsSlugCwdContentAndTimes", func(t TB, s Store) {
		write(t, s, projA, "p", "project body", false)
		write(t, s, projA, "g", "global body", true)
		m, found, err := s.Get(projA, "p", false)
		if err != nil || !found {
			t.Fatalf("Get: %v found=%v", err, found)
		}
		if m.Slug != "p" || m.CWD != projA || m.Content != "project body" || m.CreatedAt == 0 || m.UpdatedAt == 0 {
			t.Errorf("Get = %+v; want slug p, cwd %s, content, both times set", m, projA)
		}
		g, found, err := s.Get(projA, "g", true)
		if err != nil || !found {
			t.Fatalf("Get global: %v found=%v", err, found)
		}
		if g.Slug != "g" || g.CWD != memory.GlobalCWD || g.Content != "global body" {
			t.Errorf("Get(global) = %+v; want cwd %s", g, memory.GlobalCWD)
		}
	}},
	{"GlobalIsASeparateScopeForWriteGetDelete", func(t TB, s Store) {
		if !write(t, s, projA, "same", "global one", true) {
			t.Error("first global Write must report created")
		}
		if _, found, _ := s.Get(projA, "same", false); found {
			t.Error("a global memory must NOT be found with global=false")
		}
		if m, found, _ := s.Get(projA, "same", true); !found || m.Content != "global one" {
			t.Errorf("a global memory must be found with global=true: %+v %v", m, found)
		}
		// The same slug as a project memory is another memory.
		if !write(t, s, projA, "same", "project one", false) {
			t.Error("a project Write with a global's slug must report created")
		}
		g, _, _ := s.Get(projA, "same", true)
		p, _, _ := s.Get(projA, "same", false)
		if g.Content != "global one" || p.Content != "project one" {
			t.Errorf("global %q / project %q; want them kept apart", g.Content, p.Content)
		}
		// Global is reached from any cwd.
		if m, found, _ := s.Get(projB, "same", true); !found || m.Content != "global one" {
			t.Errorf("a global memory must be found from another cwd: %+v %v", m, found)
		}
		if d, _ := s.Delete(projA, "same", false); !d {
			t.Error("deleting the project memory must report true")
		}
		if _, found, _ := s.Get(projA, "same", true); !found {
			t.Error("deleting the project memory must leave the global one")
		}
		if d, _ := s.Delete(projB, "same", true); !d {
			t.Error("deleting the global memory (from any cwd) must report true")
		}
		if _, found, _ := s.Get(projA, "same", true); found {
			t.Error("the global memory is still there after Delete(global)")
		}
	}},
	{"ProjectsAreIsolated", func(t TB, s Store) {
		write(t, s, projA, "same", "in a", false)
		if !write(t, s, projB, "same", "in b", false) {
			t.Error("the same slug in another cwd is a new memory: created must be true")
		}
		a, _, _ := s.Get(projA, "same", false)
		b, _, _ := s.Get(projB, "same", false)
		if a.Content != "in a" || b.Content != "in b" || a.CWD != projA || b.CWD != projB {
			t.Errorf("a = %+v, b = %+v; want two separate memories", a, b)
		}
		if d, _ := s.Delete(projA, "same", false); !d {
			t.Fatal("Delete must report true")
		}
		if _, found, _ := s.Get(projB, "same", false); !found {
			t.Error("deleting in one project must leave the other's")
		}
	}},
	{"DeleteReportsWhetherOneExisted", func(t TB, s Store) {
		write(t, s, projA, "x", "body", false)
		d, err := s.Delete(projA, "x", false)
		if err != nil || !d {
			t.Fatalf("Delete(existing) = %v, %v; want true, nil", d, err)
		}
		if _, found, _ := s.Get(projA, "x", false); found {
			t.Error("still present after Delete")
		}
		d, err = s.Delete(projA, "x", false)
		if err != nil || d {
			t.Errorf("Delete(missing) = %v, %v; want false, nil", d, err)
		}
		d, err = s.Delete(projA, "x", true)
		if err != nil || d {
			t.Errorf("Delete(missing global) = %v, %v; want false, nil", d, err)
		}
		if r := search(t, s, "", "", false, 0, 0); r.Total != 0 {
			t.Errorf("total after deleting everything = %d, want 0", r.Total)
		}
	}},
	{"ListScopes", func(t TB, s Store) {
		write(t, s, projA, "a1", "x", false)
		write(t, s, projA, "a2", "x", false)
		write(t, s, projB, "b1", "x", false)
		write(t, s, projA, "g1", "x", true)
		wantSet(t, `cwd "" (all projects, global included)`, search(t, s, "", "", false, 0, 0), "a1", "a2", "b1", "g1")
		wantSet(t, "cwd <global> (only global)", search(t, s, memory.GlobalCWD, "", false, 0, 0), "g1")
		wantSet(t, "a project (its own plus global)", search(t, s, projA, "", false, 0, 0), "a1", "a2", "g1")
		wantSet(t, "another project (its own plus global)", search(t, s, projB, "", false, 0, 0), "b1", "g1")
		wantSet(t, "a project with nothing of its own", search(t, s, "/work/empty", "", false, 0, 0), "g1")
	}},
	{"ListIsOrderedByUpdatedDescendingWithoutScore", func(t TB, s Store) {
		write(t, s, projA, "a", "x", false)
		tick()
		write(t, s, projA, "b", "x", false)
		tick()
		write(t, s, projA, "c", "x", false)
		tick()
		write(t, s, projA, "a", "x again", false) // a is now the most recent
		r := search(t, s, projA, "", false, 0, 0)
		if got := slugs(r); !slices.Equal(got, []string{"a", "c", "b"}) {
			t.Fatalf("list order = %v; want [a c b] (most recently updated first)", got)
		}
		for _, m := range r.Results {
			if m.Score != 0 {
				t.Errorf("list mode must not set a score: %s has %v", m.Slug, m.Score)
			}
		}
		// Global memories are ordered with the rest.
		tick()
		write(t, s, projA, "g", "x", true)
		if got := slugs(search(t, s, projA, "", false, 0, 0)); !slices.Equal(got, []string{"g", "a", "c", "b"}) {
			t.Errorf("order with a global = %v; want [g a c b]", got)
		}
	}},
	{"TextSearchFindsWholeWordsCaseInsensitively", func(t TB, s Store) {
		write(t, s, projA, "m1", "the zebra crossed the road", false)
		write(t, s, projA, "m2", "ZEBRA in capitals", false)
		write(t, s, projA, "m3", "a Zebra, with punctuation.", false)
		write(t, s, projA, "m4", "nothing about animals here", false)
		write(t, s, projA, "m5", "the giraffe is tall", false)
		for _, q := range []string{"zebra", "ZEBRA", "Zebra"} {
			wantSet(t, "search "+q, search(t, s, projA, q, false, 0, 0), "m1", "m2", "m3")
		}
		wantSet(t, "search giraffe", search(t, s, projA, "giraffe", false, 0, 0), "m5")
		wantSet(t, "search of a word nobody has", search(t, s, projA, "walrus", false, 0, 0))
	}},
	{"TextSearchHonoursScope", func(t TB, s Store) {
		write(t, s, projA, "a", "the zebra of a", false)
		write(t, s, projB, "b", "the zebra of b", false)
		write(t, s, projA, "g", "the global zebra", true)
		write(t, s, projA, "other", "no animals", false)
		wantSet(t, `cwd ""`, search(t, s, "", "zebra", false, 0, 0), "a", "b", "g")
		wantSet(t, "cwd <global>", search(t, s, memory.GlobalCWD, "zebra", false, 0, 0), "g")
		wantSet(t, "project a: its own plus global", search(t, s, projA, "zebra", false, 0, 0), "a", "g")
		wantSet(t, "project b: its own plus global", search(t, s, projB, "zebra", false, 0, 0), "b", "g")
	}},
	{"RelevanceIsOrderedByRepeatedWord", func(t TB, s Store) {
		// Two words, two write orders: the repeated one must win whether it
		// was written before or after the memory that mentions it once.
		write(t, s, projA, "r1-alpha-many", "alpha alpha alpha alpha alpha", false)
		tick()
		write(t, s, projA, "r0-alpha-once", "alpha mentioned only once here", false)
		tick()
		write(t, s, projA, "r2-beta-once", "beta mentioned only once here", false)
		tick()
		write(t, s, projA, "r3-beta-many", "beta beta beta beta beta", false)
		for i := range 3 {
			tick()
			write(t, s, projA, fmt.Sprintf("filler-%d", i), "completely unrelated text", false)
		}
		for _, c := range []struct{ word, many, once string }{
			{"alpha", "r1-alpha-many", "r0-alpha-once"},
			{"beta", "r3-beta-many", "r2-beta-once"},
		} {
			r := search(t, s, projA, c.word, true, 0, 0)
			got := slugs(r)
			if len(got) != 2 || got[0] != c.many || got[1] != c.once {
				t.Errorf("search %s = %v; want [%s %s] (the repeated word ranks first)", c.word, got, c.many, c.once)
			}
			for i := 1; i < len(r.Results); i++ {
				if r.Results[i].Score > r.Results[i-1].Score {
					t.Errorf("search %s: scores must not increase down the list: %v then %v", c.word, r.Results[i-1].Score, r.Results[i].Score)
				}
			}
		}
	}},
	{"LimitDefaultsAndSkipClampsAreEchoed", func(t TB, s Store) {
		for i := range 25 {
			write(t, s, projA, fmt.Sprintf("m%02d", i), "common word", false)
			tick()
		}
		for _, limit := range []int{0, -3} {
			r := search(t, s, projA, "", false, 0, limit)
			if r.Limit != 10 || r.Returned != 10 || len(r.Results) != 10 || r.Total != 25 {
				t.Errorf("limit %d: limit=%d returned=%d len=%d total=%d; want 10, 10, 10, 25", limit, r.Limit, r.Returned, len(r.Results), r.Total)
			}
		}
		r := search(t, s, projA, "", false, -5, 4)
		if r.Skip != 0 || r.Limit != 4 || r.Returned != 4 || r.Total != 25 {
			t.Errorf("skip -5, limit 4: skip=%d limit=%d returned=%d total=%d; want 0, 4, 4, 25", r.Skip, r.Limit, r.Returned, r.Total)
		}
		r = search(t, s, projA, "common", false, 3, 0)
		if r.Skip != 3 || r.Limit != 10 || r.Returned != 10 || r.Total != 25 {
			t.Errorf("text, skip 3: skip=%d limit=%d returned=%d total=%d; want 3, 10, 10, 25", r.Skip, r.Limit, r.Returned, r.Total)
		}
		r = search(t, s, projA, "", false, 20, 10)
		if r.Skip != 20 || r.Returned != 5 || r.Total != 25 {
			t.Errorf("last page: skip=%d returned=%d total=%d; want 20, 5, 25", r.Skip, r.Returned, r.Total)
		}
		r = search(t, s, projA, "", false, 100, 10)
		if r.Returned != 0 || len(r.Results) != 0 || r.Total != 25 {
			t.Errorf("past the end: returned=%d total=%d; want 0 and total 25", r.Returned, r.Total)
		}
	}},
	{"PagesDoNotOverlapAndConcatenateToTheWhole", func(t TB, s Store) {
		for i := range 23 {
			write(t, s, projA, fmt.Sprintf("m%02d", i), "common word", false)
			tick()
		}
		// List mode has a defined order, so the pages must rebuild it exactly.
		full := slugs(search(t, s, projA, "", false, 0, 100))
		if len(full) != 23 {
			t.Fatalf("full list has %d, want 23", len(full))
		}
		var joined []string
		for skip := 0; skip < 23; skip += 7 {
			r := search(t, s, projA, "", false, skip, 7)
			if r.Total != 23 || r.Returned != len(r.Results) {
				t.Errorf("page at %d: total=%d returned=%d len=%d", skip, r.Total, r.Returned, len(r.Results))
			}
			joined = append(joined, slugs(r)...)
		}
		if !slices.Equal(joined, full) {
			t.Errorf("list pages = %v\nfull   = %v", joined, full)
		}
		// Text mode: no ranking among equals is promised, but no memory may
		// repeat or go missing across pages.
		var seen []string
		for skip := 0; skip < 23; skip += 5 {
			r := search(t, s, projA, "common", false, skip, 5)
			if r.Total != 23 {
				t.Errorf("text page at %d: total=%d, want 23", skip, r.Total)
			}
			seen = append(seen, slugs(r)...)
		}
		if !slices.Equal(sorted(seen), sorted(full)) {
			t.Errorf("text pages hold %v; want each of the 23 memories exactly once", sorted(seen))
		}
	}},
	{"IncludeContentFalseLeavesContentEmpty", func(t TB, s Store) {
		write(t, s, projA, "m", "the zebra body", false)
		for _, q := range []string{"", "zebra"} {
			r := search(t, s, projA, q, false, 0, 0)
			if len(r.Results) != 1 || r.Results[0].Content != "" || r.Results[0].Slug != "m" {
				t.Errorf("query %q, include_content=false: %+v; want slug m with empty content", q, r.Results)
			}
			r = search(t, s, projA, q, true, 0, 0)
			if len(r.Results) != 1 || r.Results[0].Content != "the zebra body" {
				t.Errorf("query %q, include_content=true: %+v; want the content", q, r.Results)
			}
		}
	}},
	{"EveryResultCarriesItsCwd", func(t TB, s Store) {
		write(t, s, projA, "a", "the zebra", false)
		write(t, s, projB, "b", "the zebra", false)
		write(t, s, projA, "g", "the zebra", true)
		want := map[string]string{"a": projA, "b": projB, "g": memory.GlobalCWD}
		for _, q := range []string{"", "zebra"} {
			for _, content := range []bool{false, true} {
				r := search(t, s, "", q, content, 0, 0)
				if len(r.Results) != 3 {
					t.Fatalf("query %q: %d results, want 3", q, len(r.Results))
				}
				for _, m := range r.Results {
					if m.CWD != want[m.Slug] {
						t.Errorf("query %q content=%v: %s has cwd %q, want %q", q, content, m.Slug, m.CWD, want[m.Slug])
					}
				}
			}
		}
	}},
	{"ConcurrentWritesLoseNothing", func(t TB, s Store) {
		const n = 30
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.Write(projA, fmt.Sprintf("m%02d", i), "concurrent body", false); err != nil {
					t.Errorf("Write %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		r, err := s.Search(projA, "", false, 0, 100)
		if err != nil || r.Total != n || r.Returned != n {
			t.Fatalf("after %d concurrent writes: total=%d returned=%d err=%v; want %d", n, r.Total, r.Returned, err, n)
		}
		for i := range n {
			if _, found, _ := s.Get(projA, fmt.Sprintf("m%02d", i), false); !found {
				t.Errorf("m%02d was lost", i)
			}
		}
	}},
}
