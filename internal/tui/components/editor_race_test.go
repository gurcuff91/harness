package components

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countRenderer counts the repaints an editor asks for.
type countRenderer struct{ n atomic.Int64 }

func (c *countRenderer) RequestRender(bool) { c.n.Add(1) }

// hammer runs write on one goroutine and read on another for d, both looping until the time is up
// and released together, so the two really overlap (a fixed iteration count lets the faster side
// finish before the other starts, and nothing races).
func hammer(d time.Duration, write, read func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	stop := make(chan struct{})
	loop := func(f func(i int)) {
		defer wg.Done()
		<-start
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				f(i)
			}
		}
	}
	wg.Add(2)
	go loop(write)
	go loop(read)
	close(start)
	time.Sleep(d)
	close(stop)
	wg.Wait()
}

// The editor is typed into by the input goroutine while the render timer's goroutine draws it (and
// draws the separator above it, through HiddenAbove). Under -race these fail on any buffer, cursor
// or placeholder access outside the lock; without -race a torn slice shows up as a panic.
func TestEditorEveryEntryPointVsRender(t *testing.T) {
	e := NewEditor(&countRenderer{}, "type...")
	keys := []string{"a", "b", "/", "\x7f", "\n", "\x1b[D", "\x1b[C", "\x1b[A", "\x1b[B", "\x01", "\x05", "\x0b", "\x15", "\x17", "\x1b[3~", "\x1b[200~pasted\ntext\x1b[201~"}
	hammer(500*time.Millisecond, func(i int) {
		switch i % 40 {
		case 37:
			e.SetValue(fmt.Sprintf("value %d with several words to wrap around the editor width", i))
		case 38:
			e.Clear()
		case 39:
			e.InsertText("/tmp/image.png")
			e.SetPlaceholder("other")
		default:
			e.HandleInput(keys[i%len(keys)])
		}
	}, func(i int) {
		w := 10 + i%70
		_ = e.Render(w)
		_ = e.HiddenAbove(w)
		_ = e.Value()
		_ = e.CursorPos()
		_ = e.Placeholder()
	})
}

// Every public method may be called from inside a callback: the palette's OnChange reads Value and
// calls Clear and SetValue; a submit runs a command that clears the editor. A mutex held across a
// callback would deadlock the first of them, so this has a deadline.
func TestEditorCallbacksMayReenterTheEditor(t *testing.T) {
	e := NewEditor(&countRenderer{}, "")
	var changes, submits, escapes int
	e.OnChange = func(text string) {
		changes++
		_ = e.Value()
		_ = e.CursorPos()
		_ = e.Render(40)
		if text == "clear-me" {
			e.Clear() // re-enters: its own OnChange runs with "" (no recursion beyond that)
		}
		if text == "grow" {
			e.SetValue("grown")
		}
	}
	e.OnSubmit = func(text string) {
		submits++
		e.Clear()
		e.InsertText("again")
	}
	e.OnEscape = func() {
		escapes++
		e.SetValue("/")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.SetValue("clear-me")
		if e.Value() != "" {
			t.Errorf("Clear from OnChange: value %q", e.Value())
		}
		e.SetValue("grow")
		if e.Value() != "grown" {
			t.Errorf("SetValue from OnChange: value %q", e.Value())
		}
		e.HandleInput("\r")
		if e.Value() != "again" {
			t.Errorf("Clear+InsertText from OnSubmit: value %q", e.Value())
		}
		e.HandleInput("\x1b")
		if e.Value() != "/" {
			t.Errorf("SetValue from OnEscape: value %q", e.Value())
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: a callback re-entering the editor never returned")
	}
	if submits != 1 || escapes != 1 {
		t.Errorf("OnSubmit ran %d times, OnEscape %d; want 1 each", submits, escapes)
	}
	// clear-me → (Clear → "") ; grow → (SetValue grown) ; submit's Clear and InsertText ; escape's SetValue
	// SetValue(clear-me)=1, Clear=1, SetValue(grow)=1, SetValue(grown)=1, Clear=1, InsertText=1, SetValue("/")=1
	if changes != 7 {
		t.Errorf("OnChange ran %d times, want 7", changes)
	}
}

// What the outside world is told, and when, is part of the contract (the palette and the repaint
// depend on it): an edit that changes the text calls OnChange once with the NEW text and asks for a
// repaint; a cursor move only repaints; Enter only submits (with the text) and Escape only escapes.
func TestEditorDeliversExactlyWhatEachEditCaused(t *testing.T) {
	r := &countRenderer{}
	e := NewEditor(r, "")
	var got []string
	e.OnChange = func(s string) { got = append(got, "change:"+s) }
	e.OnSubmit = func(s string) { got = append(got, "submit:"+s) }
	e.OnEscape = func() { got = append(got, "escape") }

	step := func(name string, f func(), wantEvents []string, wantRepaints int64) {
		t.Helper()
		got = nil
		before := r.n.Load()
		f()
		if fmt.Sprint(got) != fmt.Sprint(wantEvents) {
			t.Errorf("%s: events %v, want %v", name, got, wantEvents)
		}
		if d := r.n.Load() - before; d != wantRepaints {
			t.Errorf("%s: %d repaints, want %d", name, d, wantRepaints)
		}
	}
	step("type", func() { e.HandleInput("a") }, []string{"change:a"}, 1)
	step("type 2", func() { e.HandleInput("b") }, []string{"change:ab"}, 1)
	step("cursor left", func() { e.HandleInput("\x1b[D") }, nil, 1)
	step("home", func() { e.HandleInput("\x01") }, nil, 1)
	step("cursor at the edge does nothing", func() { e.HandleInput("\x1b[D") }, nil, 0)
	step("backspace at 0 does nothing", func() { e.HandleInput("\x7f") }, nil, 0)
	step("delete forward", func() { e.HandleInput("\x1b[3~") }, []string{"change:b"}, 1)
	step("paste", func() { e.HandleInput("\x1b[200~x\r\ny\x1b[201~") }, []string{"change:x\nyb"}, 1)
	step("set", func() { e.SetValue("hello") }, []string{"change:hello"}, 1)
	step("submit", func() { e.HandleInput("\r") }, []string{"submit:hello"}, 0)
	step("escape", func() { e.HandleInput("\x1b") }, []string{"escape"}, 0)
	step("clear", func() { e.Clear() }, []string{"change:"}, 1)
	step("insert text", func() { e.InsertText("/p.png") }, []string{"change:/p.png"}, 1)
	step("insert text adds a separating space", func() { e.InsertText("/q.png") }, []string{"change:/p.png /q.png"}, 1)
	step("ctrl+c is not the editor's", func() { e.HandleInput("\x03") }, nil, 0)
	step("a cursor move that does not move", func() { e.HandleInput("\x1b[C") }, nil, 0)
}

// History is appended to by the event-stream goroutine and rendered by the render timer's.
func TestHistoryEveryEntryPointVsRender(t *testing.T) {
	h := NewHistory()
	hammer(500*time.Millisecond, func(i int) {
		switch i % 50 {
		case 49:
			h.Clear()
		case 48:
			h.Invalidate()
		default:
			h.Add(NewRawBlock(fmt.Sprintf("block %d", i)))
		}
	}, func(i int) {
		_ = h.Render(20 + i%60)
		_ = h.Blocks()
		_ = h.Len()
		_ = h.Last()
		if i%7 == 0 {
			h.Invalidate() // the resize path runs on the render side too
		}
	})
}

// Typing must never wait for a frame to be drawn: layout takes its snapshot under the lock and
// lays out after releasing it. With a very large buffer a layout takes long enough to be told apart
// from the cost of the snapshot itself: while one goroutine renders it in a loop, the MEDIAN time of
// an O(1) call (CursorPos) on another must stay far below one layout. (The median, not the worst
// case: scheduler noise inflates single samples, but a lock held through drawing inflates nearly
// all of them.)
func TestEditorRenderDoesNotHoldTheLockWhileLaying(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue(strings.Repeat("some words to wrap around the editor width ", 4000))

	t0 := time.Now()
	for i := 0; i < 3; i++ {
		e.Render(60)
	}
	layout := time.Since(t0) / 3
	if layout < 5*time.Millisecond {
		t.Skipf("a layout takes only %v here: too fast to tell a held lock from a snapshot", layout)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				e.Render(60)
			}
		}
	}()
	time.Sleep(layout) // let the render loop get going
	samples := make([]time.Duration, 0, 400)
	for i := 0; i < 400; i++ {
		s := time.Now()
		e.CursorPos()
		samples = append(samples, time.Since(s))
		time.Sleep(layout / 50)
	}
	close(stop)
	<-done
	sort.Slice(samples, func(a, b int) bool { return samples[a] < samples[b] })
	median := samples[len(samples)/2]
	if median > layout/4 {
		t.Errorf("median CursorPos latency %v during a render loop, one layout takes %v: the lock is held while drawing", median, layout)
	}
}

// Edits that rewrite the buffer IN PLACE (backspace, delete, Ctrl+U/K/W shift the runes inside the
// same array) must not be visible to a layout that is reading it: layout copies the buffer under
// the lock. The buffer is kept long so there is always something to shift and something to read.
func TestEditorInPlaceEditsVsRender(t *testing.T) {
	e := NewEditor(nil, "")
	e.SetValue(strings.Repeat("abcdefghij ", 200))
	hammer(500*time.Millisecond, func(i int) {
		switch i % 4 {
		case 0:
			e.HandleInput("\x7f") // backspace: append(buf[:c-1], buf[c:]...) in place
		case 1:
			e.HandleInput("\x1b[3~") // delete forward, in place
		case 2:
			e.HandleInput("\x1b[D")
		default:
			e.InsertText("xy") // grows it back
		}
	}, func(i int) {
		_ = e.Render(40)
		_ = e.HiddenAbove(40)
	})
}
