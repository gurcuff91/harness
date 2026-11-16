package components

import (
	"fmt"
	"strings"
	"testing"
)

// values lists the Values of the filtered entries, in order.
func rankValues(l *SelectList) []string {
	var out []string
	for i := 0; ; i++ {
		l.mu.Lock()
		if i >= len(l.filtered) {
			l.mu.Unlock()
			return out
		}
		out = append(out, l.filtered[i].Value)
		l.mu.Unlock()
	}
}

// The palette's own commands, with the descriptions it really shows.
func paletteLikeItems() []SelectItem {
	return []SelectItem{
		{Value: "connect", Label: "connect", Description: "Connect a provider"},
		{Value: "disconnect", Label: "disconnect", Description: "Disconnect a provider"},
		{Value: "resume", Label: "resume", Description: "Resume a previous session"},
		{Value: "delete", Label: "delete", Description: "Delete a session"},
		{Value: "info", Label: "info", Description: "Show session info snapshot"},
		{Value: "context", Label: "context", Description: "Show context window breakdown"},
		{Value: "fork", Label: "fork", Description: "Fork this session — exact copy with a new ID"},
		{Value: "new", Label: "new", Description: "Close the active session (if any) and start a brand new, empty one"},
		{Value: "model", Label: "model", Description: "Switch model"},
		{Value: "thinking", Label: "thinking", Description: "Set the thinking level"},
		{Value: "rename", Label: "rename", Description: "Rename this session"},
		{Value: "quit", Label: "quit", Description: "Exit harness"},
	}
}

// What Tab completes and Enter runs is the FIRST entry: a command whose NAME starts with what was
// typed must come before one that only mentions it in its description. This is the reported case:
// "/thi" completed to /fork because fork's description says "Fork THIs session".
func TestFilterRanksTheNameBeforeTheDescription(t *testing.T) {
	l := NewSelectList(paletteLikeItems(), 8)
	l.SetFilter("thi")
	got := rankValues(l)
	if len(got) == 0 || got[0] != "thinking" {
		t.Fatalf("'thi' ranks %v, want thinking first", got)
	}
	// the description-only matches are still offered, after the name matches
	if strings.Join(got, ",") != "thinking,rename,fork" && !contains(got, "fork") {
		t.Fatalf("'thi' dropped the description-only matches: %v", got)
	}
	if i, j := idx(got, "thinking"), idx(got, "fork"); i > j {
		t.Fatalf("thinking (%d) ranks after fork (%d): %v", i, j, got)
	}
}

func contains(s []string, v string) bool { return idx(s, v) >= 0 }
func idx(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestFilterRankOrder(t *testing.T) {
	items := []SelectItem{
		{Value: "a-desc", Label: "a-desc", Description: "mentions xyz here"},
		{Value: "b-contains", Label: "b-contains", Description: "nothing"},
		{Value: "xyz-prefix", Label: "xyz-prefix", Description: "nothing"},
		{Value: "c-desc", Label: "c-desc", Description: "xyz again"},
		{Value: "d-prefix", Label: "xyzzy label", Description: "nothing"},
		{Value: "e-contains", Label: "label with xyz inside", Description: "nothing"},
		{Value: "none", Label: "none", Description: "nothing"},
	}
	// make b-contains really contain xyz in its name
	items[1].Label, items[1].Value = "b-xyz-contains", "b-xyz-contains"
	l := NewSelectList(items, 8)
	l.SetFilter("XYZ") // case-insensitive
	want := "xyz-prefix,d-prefix,b-xyz-contains,e-contains,a-desc,c-desc"
	if got := strings.Join(rankValues(l), ","); got != want {
		t.Fatalf("ranking:\n got %s\nwant %s", got, want)
	}
	// the highlighted entry is the best match
	if it, ok := l.Selected(); !ok || it.Value != "xyz-prefix" {
		t.Fatalf("selected %v %v, want xyz-prefix", it, ok)
	}
	if l.Count() != 6 {
		t.Fatalf("count %d, want 6", l.Count())
	}
}

// Value counts as the name too (a session whose Label differs from its Value), and so does the
// Label when the Value is something else (an id).
func TestFilterRankUsesLabelAndValueAsTheName(t *testing.T) {
	l := NewSelectList([]SelectItem{
		{Value: "zzz", Label: "alpha", Description: "beta is described here"},
		{Value: "beta-id", Label: "other", Description: "x"},
		{Value: "q", Label: "beta-label", Description: "x"},
	}, 8)
	l.SetFilter("beta")
	if got, want := strings.Join(rankValues(l), ","), "beta-id,q,zzz"; got != want {
		t.Fatalf("ranking %s, want %s", got, want)
	}
}

// Within a rank, the original order is kept.
func TestFilterRankKeepsTheOriginalOrderWithinARank(t *testing.T) {
	var items []SelectItem
	for i := 0; i < 20; i++ {
		items = append(items, SelectItem{Value: fmt.Sprintf("cmd%02d", i), Label: fmt.Sprintf("cmd%02d", i), Description: "d"})
	}
	l := NewSelectList(items, 8)
	l.SetFilter("cmd")
	got := rankValues(l)
	for i, v := range got {
		if v != fmt.Sprintf("cmd%02d", i) {
			t.Fatalf("entry %d is %s: the order within a rank changed: %v", i, v, got)
		}
	}
}

// The no-match and empty-filter behaviour is untouched.
func TestFilterRankNoMatchAndEmpty(t *testing.T) {
	l := NewSelectList(paletteLikeItems(), 8)
	l.SetFilter("zzzz")
	if l.Count() != 0 || len(rankValues(l)) != 0 {
		t.Fatalf("a filter that matches nothing left %d entries", l.Count())
	}
	if _, ok := l.Selected(); ok {
		t.Fatal("an empty list has a selection")
	}
	l.SetFilter("")
	if l.Count() != len(paletteLikeItems()) {
		t.Fatalf("clearing the filter shows %d entries", l.Count())
	}
	if got := rankValues(l); got[0] != "connect" || got[len(got)-1] != "quit" {
		t.Fatalf("the unfiltered order changed: %v", got)
	}
}

// Filtering twice does not accumulate: each filter works from the full item set, not from the
// previous result, and the underlying items are never reordered.
func TestFilterRankIsAlwaysFromTheFullSet(t *testing.T) {
	l := NewSelectList(paletteLikeItems(), 8)
	l.SetFilter("thi")
	l.SetFilter("con")
	if got := rankValues(l); len(got) == 0 || got[0] != "connect" {
		t.Fatalf("'con' ranks %v after 'thi'", got)
	}
	l.SetFilter("")
	if got := rankValues(l); len(got) != len(paletteLikeItems()) || got[0] != "connect" {
		t.Fatalf("items were disturbed: %v", got)
	}
}

// A name that CONTAINS the filter in the middle is a name match even when it does not start with
// it, for the Label and for the Value alike — and it outranks a description-only match whichever
// field it is in.
func TestFilterRankMiddleOfTheNameBeatsTheDescription(t *testing.T) {
	l := NewSelectList([]SelectItem{
		{Value: "d1", Label: "d1", Description: "has mid inside"},
		{Value: "x-mid-value", Label: "unrelated label", Description: "x"},
		{Value: "unrelated-value", Label: "x-mid-label", Description: "x"},
	}, 8)
	l.SetFilter("mid")
	if got, want := strings.Join(rankValues(l), ","), "x-mid-value,unrelated-value,d1"; got != want {
		t.Fatalf("ranking %s, want %s", got, want)
	}
	// and a Value-only middle match is found at all (not only ranked) when nothing else mentions it
	l2 := NewSelectList([]SelectItem{{Value: "a-zq-b", Label: "plain", Description: "nothing"}}, 8)
	l2.SetFilter("zq")
	if l2.Count() != 1 {
		t.Fatalf("a Value containing the filter was dropped: %d entries", l2.Count())
	}
}
