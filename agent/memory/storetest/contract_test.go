package storetest

import (
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/memory"
)

// The suite's Store and memory.Store are the same method set, both ways.
var (
	_ Store        = memory.Store(nil)
	_ memory.Store = Store(nil)
)

type recorder struct {
	mu     sync.Mutex
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) fail()                 { r.mu.Lock(); r.failed = true; r.mu.Unlock() }
func (r *recorder) Error(...any)          { r.fail() }
func (r *recorder) Errorf(string, ...any) { r.fail() }
func (r *recorder) Fatal(...any)          { r.fail(); runtime.Goexit() }
func (r *recorder) Fatalf(string, ...any) { r.fail(); runtime.Goexit() }

// failedCases returns the names of the cases that fail on a fresh store.
func failedCases(newStore func() Store) []string {
	var out []string
	for _, c := range Cases {
		r := &recorder{}
		s := newStore()
		done := make(chan struct{})
		go func() { defer close(done); c.Run(r, s) }()
		<-done
		s.Close()
		if r.failed {
			out = append(out, c.Name)
		}
	}
	return out
}

func TestSuitePassesOnReference(t *testing.T) {
	RunContract(t, func(*testing.T) Store { return NewReference() })
}

func TestCaseNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Cases {
		if seen[c.Name] {
			t.Errorf("duplicate case %q", c.Name)
		}
		seen[c.Name] = true
	}
}

// ── broken stores: each breaks one rule ─────────────────────────────────

type ref = *Reference

// ignoresGlobal stores a global memory under the project cwd.
type ignoresGlobal struct{ ref }

func (b ignoresGlobal) Write(c, s, v string, _ bool) (bool, error) {
	return b.ref.Write(c, s, v, false)
}
func (b ignoresGlobal) Get(c, s string, _ bool) (memory.Memory, bool, error) {
	return b.ref.Get(c, s, false)
}
func (b ignoresGlobal) Delete(c, s string, _ bool) (bool, error) { return b.ref.Delete(c, s, false) }

// noGlobalInProject drops the global memories from a project's search.
type noGlobalInProject struct{ ref }

func (b noGlobalInProject) Search(cwd, q string, inc bool, skip, limit int) (memory.SearchResult, error) {
	r, err := b.ref.Search(cwd, q, inc, skip, limit)
	if cwd == "" || cwd == memory.GlobalCWD {
		return r, err
	}
	kept := []memory.Memory{}
	for _, m := range r.Results {
		if m.CWD != memory.GlobalCWD {
			kept = append(kept, m)
		}
	}
	r.Total -= len(r.Results) - len(kept)
	r.Results, r.Returned = kept, len(kept)
	return r, err
}

// listBySlug orders list mode by slug instead of updated_at.
type listBySlug struct{ ref }

func (b listBySlug) Search(cwd, q string, inc bool, skip, limit int) (memory.SearchResult, error) {
	if q != "" {
		return b.ref.Search(cwd, q, inc, skip, limit)
	}
	r, err := b.ref.Search(cwd, q, inc, 0, 100000)
	sort.Slice(r.Results, func(i, j int) bool { return r.Results[i].Slug < r.Results[j].Slug })
	if limit <= 0 {
		limit = 10
	}
	if skip < 0 {
		skip = 0
	}
	end := min(skip+limit, len(r.Results))
	if skip > end {
		skip = end
	}
	r.Results = r.Results[skip:end]
	r.Skip, r.Limit, r.Returned = skip, limit, len(r.Results)
	return r, err
}

// matchesEverything answers a text search with the whole list.
type matchesEverything struct{ ref }

func (b matchesEverything) Search(cwd, q string, inc bool, skip, limit int) (memory.SearchResult, error) {
	return b.ref.Search(cwd, "", inc, skip, limit)
}

// overlappingPages starts every page one entry early.
type overlappingPages struct{ ref }

func (b overlappingPages) Search(cwd, q string, inc bool, skip, limit int) (memory.SearchResult, error) {
	r, err := b.ref.Search(cwd, q, inc, max(skip-1, 0), limit)
	r.Skip = max(skip, 0)
	return r, err
}

// unrankedText returns text matches in the order of the list (ignores relevance).
type unrankedText struct{ ref }

func (b unrankedText) Search(cwd, q string, inc bool, skip, limit int) (memory.SearchResult, error) {
	r, err := b.ref.Search(cwd, q, inc, skip, limit)
	if q != "" {
		sort.Slice(r.Results, func(i, j int) bool { return r.Results[i].Slug < r.Results[j].Slug })
	}
	return r, err
}

// alwaysCreated / alwaysDeleted lie about the outcome.
type alwaysCreated struct{ ref }

func (b alwaysCreated) Write(c, s, v string, g bool) (bool, error) {
	_, err := b.ref.Write(c, s, v, g)
	return true, err
}

type alwaysDeleted struct{ ref }

func (b alwaysDeleted) Delete(c, s string, g bool) (bool, error) {
	_, err := b.ref.Delete(c, s, g)
	return true, err
}

// leaky is a copy-on-write list read and replaced without a lock: concurrent
// writes overwrite each other (atomics keep the race detector quiet — the loss
// is a logic error, not a data race).
type leaky struct {
	ref
	rows atomic.Pointer[[]memory.Memory]
}

func (b *leaky) Write(c, s, v string, g bool) (bool, error) {
	cur := b.rows.Load()
	var next []memory.Memory
	if cur != nil {
		next = append(next, *cur...)
	}
	time.Sleep(time.Millisecond) // widen the window
	next = append(next, memory.Memory{Slug: s, CWD: c, Content: v})
	b.rows.Store(&next)
	return true, nil
}
func (b *leaky) Get(c, s string, g bool) (memory.Memory, bool, error) {
	if cur := b.rows.Load(); cur != nil {
		for _, m := range *cur {
			if m.CWD == c && m.Slug == s {
				return m, true, nil
			}
		}
	}
	return memory.Memory{}, false, nil
}
func (b *leaky) Search(cwd, q string, inc bool, skip, limit int) (memory.SearchResult, error) {
	var all []memory.Memory
	if cur := b.rows.Load(); cur != nil {
		all = *cur
	}
	return memory.SearchResult{Total: len(all), Returned: len(all), Results: all}, nil
}

func TestSuiteCatchesBrokenStores(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mk      func() Store
		mustHit []string // cases that must fail (at least these)
	}{
		{"ignores global", func() Store { return ignoresGlobal{NewReference()} }, []string{"GlobalIsASeparateScopeForWriteGetDelete"}},
		{"project search forgets global", func() Store { return noGlobalInProject{NewReference()} }, []string{"ListScopes", "TextSearchHonoursScope"}},
		{"list ordered by slug", func() Store { return listBySlug{NewReference()} }, []string{"ListIsOrderedByUpdatedDescendingWithoutScore"}},
		{"text search returns non-matching", func() Store { return matchesEverything{NewReference()} }, []string{"TextSearchFindsWholeWordsCaseInsensitively", "TextSearchHonoursScope"}},
		{"relevance ignored", func() Store { return unrankedText{NewReference()} }, []string{"RelevanceIsOrderedByRepeatedWord"}},
		{"overlapping pagination", func() Store { return overlappingPages{NewReference()} }, []string{"PagesDoNotOverlapAndConcatenateToTheWhole"}},
		{"always created", func() Store { return alwaysCreated{NewReference()} }, []string{"WriteIsUpsertAndReportsCreated"}},
		{"always deleted", func() Store { return alwaysDeleted{NewReference()} }, []string{"DeleteReportsWhetherOneExisted"}},
		{"loses concurrent writes", func() Store { return &leaky{ref: NewReference()} }, []string{"ConcurrentWritesLoseNothing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failed := failedCases(tc.mk)
			if len(failed) == 0 {
				t.Fatal("the suite does not catch this store")
			}
			for _, want := range tc.mustHit {
				found := false
				for _, f := range failed {
					found = found || f == want
				}
				if !found {
					t.Errorf("case %s did not fail (failed: %v)", want, failed)
				}
			}
			t.Logf("caught by: %v", failed)
		})
	}
}
