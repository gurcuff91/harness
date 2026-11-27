package storetest

import (
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gurcuff91/harness/agent/memory"
)

// Reference is a minimal correct in-memory memory.Store for tests of other
// implementations (an RPC adapter's backing fake). It is NOT a production
// store: nothing is persisted and a search scans everything.
//
// Matching is by WHOLE word, case-insensitive, words being the runs of
// letters and digits of the slug and the content; every word of the query must
// be present. Relevance is the term frequency (summed over the query's words),
// ties broken by recency; list mode is most-recently-updated first.
type Reference struct {
	mu   sync.Mutex
	seq  int64
	rows map[[2]string]*refRow
}

type refRow struct {
	mem memory.Memory
	seq int64 // write order: a stable tie-break finer than a millisecond
}

// NewReference returns an empty Reference.
func NewReference() *Reference { return &Reference{rows: map[[2]string]*refRow{}} }

func scopeCWD(cwd string, global bool) string {
	if global {
		return memory.GlobalCWD
	}
	return cwd
}

// Write implements memory.Store.
func (r *Reference) Write(cwd, slug, content string, global bool) (bool, error) {
	cwd = scopeCWD(cwd, global)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UnixMilli()
	r.seq++
	key := [2]string{cwd, slug}
	if row, ok := r.rows[key]; ok {
		row.mem.Content, row.mem.UpdatedAt, row.seq = content, now, r.seq
		return false, nil
	}
	r.rows[key] = &refRow{seq: r.seq, mem: memory.Memory{Slug: slug, CWD: cwd, Content: content, CreatedAt: now, UpdatedAt: now}}
	return true, nil
}

// Get implements memory.Store.
func (r *Reference) Get(cwd, slug string, global bool) (memory.Memory, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[[2]string{scopeCWD(cwd, global), slug}]
	if !ok {
		return memory.Memory{}, false, nil
	}
	return row.mem, true, nil
}

// Delete implements memory.Store.
func (r *Reference) Delete(cwd, slug string, global bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]string{scopeCWD(cwd, global), slug}
	_, ok := r.rows[key]
	delete(r.rows, key)
	return ok, nil
}

// Close implements memory.Store.
func (r *Reference) Close() error { return nil }

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) })
}

// Search implements memory.Store.
func (r *Reference) Search(cwd, query string, includeContent bool, skip, limit int) (memory.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if skip < 0 {
		skip = 0
	}
	terms := words(query)
	searching := strings.TrimSpace(query) != ""

	type hit struct {
		mem   memory.Memory
		seq   int64
		score float64
	}
	var hits []hit
	r.mu.Lock()
	for _, row := range r.rows {
		if cwd != "" && row.mem.CWD != cwd && row.mem.CWD != memory.GlobalCWD {
			continue
		}
		h := hit{mem: row.mem, seq: row.seq}
		if searching {
			if len(terms) == 0 {
				continue
			}
			freq := map[string]int{}
			for _, w := range append(words(row.mem.Slug), words(row.mem.Content)...) {
				freq[w]++
			}
			ok := true
			for _, term := range terms {
				if freq[term] == 0 {
					ok = false
					break
				}
				h.score += float64(freq[term])
			}
			if !ok {
				continue
			}
		}
		hits = append(hits, h)
	}
	r.mu.Unlock()

	sort.Slice(hits, func(i, j int) bool {
		if searching && hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].seq > hits[j].seq
	})
	res := memory.SearchResult{Total: len(hits), Skip: skip, Limit: limit, Results: []memory.Memory{}}
	for i := skip; i < len(hits) && i < skip+limit; i++ {
		m := hits[i].mem
		m.Score = hits[i].score
		if !includeContent {
			m.Content = ""
		}
		res.Results = append(res.Results, m)
	}
	res.Returned = len(res.Results)
	return res, nil
}
