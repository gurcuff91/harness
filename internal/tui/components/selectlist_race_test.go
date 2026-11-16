package components

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// raceItems is a list shaped like the command palette's: dozens of entries, of which a typed filter
// keeps only a couple — the shrink that made Render index past the new length.
func raceItems(n int) []SelectItem {
	items := make([]SelectItem, n)
	for i := range items {
		items[i] = SelectItem{Value: fmt.Sprintf("cmd%d", i), Label: fmt.Sprintf("cmd%d", i), Description: "a description of the command"}
	}
	return items
}

// TestSelectListFilterVsRender is the panic seen live while typing /thinking: the input goroutine
// swaps the filtered slice (SetFilter) while the render timer's goroutine draws it (Render). Render
// read len(s.filtered) to bound its loop and then indexed s.filtered[i], so a swap in between ran
// the index past the new, shorter slice: "index out of range [5] with length 2". Under -race the same
// test reports the DATA RACE even on runs that happen not to panic.
func TestSelectListFilterVsRender(t *testing.T) {
	// "cmd5" matches cmd5 and cmd50..cmd59 of 60 (11 entries); "cmd59" matches ONE; both are well
	// short of the 60 the full list has, which is the shrink that matters.
	l := NewSelectList(raceItems(60), 8)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the input goroutine: typing and deleting a filter
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			l.SetFilter("cmd59") // 60 entries -> 1
			l.SetFilter("")      // and back to 60
		}
		close(stop)
	}()
	go func() { // the render timer's goroutine
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				l.Render(100)
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("timed out")
	}
}

// Navigation, selection reads and SetItems race with Render too.
func TestSelectListEveryEntryPointVsRender(t *testing.T) {
	l := NewSelectList(raceItems(40), 8)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	work := func(fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					fn(i)
				}
			}
		}()
	}
	work(func(i int) { l.Render(80) })
	work(func(i int) { l.Render(30) })
	work(func(i int) { l.HandleInput("\x1b[B") })
	work(func(i int) { l.HandleInput("\x1b[A") })
	work(func(i int) { l.Selected(); l.Count() })
	work(func(i int) {
		if i%2 == 0 {
			l.SetItems(raceItems(3))
		} else {
			l.SetItems(raceItems(50))
		}
		l.SetFilter("cmd1")
	})
	time.Sleep(700 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// OnSelect, OnCancel and OnSelectionChange re-enter the list (the palette's onEnter calls SetItems;
// notifying a change calls Selected). A mutex held across a callback would deadlock the first time.
func TestSelectListCallbacksMayReenterTheList(t *testing.T) {
	l := NewSelectList(raceItems(10), 8)
	var changes, selects, cancels int
	l.OnSelectionChange = func(it SelectItem) {
		changes++
		if _, ok := l.Selected(); !ok { // re-enters: reads the list
			t.Error("Selected() inside OnSelectionChange found nothing")
		}
		l.Count()
	}
	l.OnSelect = func(it SelectItem) {
		selects++
		l.SetItems(raceItems(4)) // re-enters: replaces the items, as palette.onEnter does
		l.SetFilter("cmd1")
		l.Render(80)
	}
	l.OnCancel = func() {
		cancels++
		l.SetItems(nil)
		l.Selected()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.HandleInput("\x1b[B")
		l.HandleInput("\x1b[B")
		l.HandleInput("\x1b[A")
		l.HandleInput("\r")
		l.HandleInput("\x1b")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: a callback re-entered the list while it was locked")
	}
	if changes != 3 || selects != 1 || cancels != 1 {
		t.Errorf("callbacks ran %d/%d/%d times (change/select/cancel), want 3/1/1", changes, selects, cancels)
	}
}
