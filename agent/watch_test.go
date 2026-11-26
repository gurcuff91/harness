package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

func drain(w *Watcher, until types.EventType) []types.Event {
	var out []types.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-w.Events():
			if !ok {
				return out
			}
			out = append(out, e)
			if e.Type == until {
				return out
			}
		case <-timeout:
			return out
		}
	}
}

func watchSession(t *testing.T) (*Agent, *Session, string) {
	a, model, _ := channelAgent(t, nil)
	s, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return a, s, model
}

func TestActiveSessionsListsOpenAndDropsClosed(t *testing.T) {
	a, model, _ := channelAgent(t, nil)
	if got := a.ActiveSessions(); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
	s1, _ := a.NewSession(t.TempDir(), model)
	s2, _ := a.NewSession(t.TempDir(), model)
	got := a.ActiveSessions()
	if len(got) != 2 || got[0] > got[1] {
		t.Fatalf("want 2 sorted ids, got %v", got)
	}
	s1.Close()
	got = a.ActiveSessions()
	if len(got) != 1 || got[0] != s2.ID() {
		t.Fatalf("want only %s, got %v", s2.ID(), got)
	}
	s2.Close()
	if got = a.ActiveSessions(); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
}

func TestWatchDeliversInOrderToTwoWatchersAndSubscribeStillWorks(t *testing.T) {
	_, s, _ := watchSession(t)
	var sub atomic.Int64
	s.Subscribe(func(types.Event) { sub.Add(1) })
	w1, w2 := s.Watch(0, nil), s.Watch(8192, nil)
	defer w1.Close()
	defer w2.Close()
	s.Prompt(context.Background(), "hi")
	e1 := drain(w1, types.EventTurnEnd)
	e2 := drain(w2, types.EventTurnEnd)
	if len(e1) == 0 || e1[0].Type != types.EventReceivedPrompt || e1[len(e1)-1].Type != types.EventTurnEnd {
		t.Fatalf("w1 events: %v", e1)
	}
	if len(e1) != len(e2) {
		t.Fatalf("watchers differ: %d vs %d", len(e1), len(e2))
	}
	for i := range e1 {
		if e1[i].Type != e2[i].Type {
			t.Fatalf("order differs at %d", i)
		}
	}
	if textOf(e1) != "answer to hi" {
		t.Fatalf("text %q", textOf(e1))
	}
	if int(sub.Load()) < len(e1) {
		t.Fatalf("subscribe saw %d < %d", sub.Load(), len(e1))
	}
}

func TestWatchStalledWatcherDropsDeltasWithoutBlocking(t *testing.T) {
	_, s, _ := watchSession(t)
	stalled := s.Watch(1, nil)
	defer stalled.Close()
	healthy := s.Watch(100, nil)
	defer healthy.Close()
	s.emit(types.Event{Type: types.EventStreamTextDelta, Delta: "a"}) // fills stalled
	start := time.Now()
	for i := 0; i < 50; i++ {
		s.emit(types.Event{Type: types.EventStreamTextDelta, Delta: "b"})
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("deltas blocked for %v", d)
	}
	if len(stalled.Events()) != 1 {
		t.Fatalf("stalled has %d", len(stalled.Events()))
	}
	if len(healthy.Events()) != 51 {
		t.Fatalf("healthy has %d", len(healthy.Events()))
	}
}

func TestWatchControlEventDroppedAfterTimeoutAndOnDrop(t *testing.T) {
	_, s, _ := watchSession(t)
	var dropped []types.Event
	w := s.Watch(1, func(e types.Event) { dropped = append(dropped, e) })
	defer w.Close()
	s.emit(types.Event{Type: types.EventLoopStart})
	start := time.Now()
	s.emit(types.Event{Type: types.EventTurnEnd})
	d := time.Since(start)
	if d < 400*time.Millisecond || d > 3*time.Second {
		t.Fatalf("control wait %v", d)
	}
	if len(dropped) != 1 || dropped[0].Type != types.EventTurnEnd {
		t.Fatalf("onDrop got %v", dropped)
	}
	// A control event gets through if room appears within the bound.
	go func() { time.Sleep(50 * time.Millisecond); <-w.Events() }()
	s.emit(types.Event{Type: types.EventLoopEnd})
	if len(dropped) != 1 {
		t.Fatalf("event was dropped despite room: %v", dropped)
	}
}

func TestWatcherCloseIsIdempotentAndClosesChannel(t *testing.T) {
	_, s, _ := watchSession(t)
	w := s.Watch(4, nil)
	w.Close()
	w.Close()
	if _, ok := <-w.Events(); ok {
		t.Fatal("channel not closed")
	}
	s.emit(types.Event{Type: types.EventTurnEnd}) // must not panic
}

func TestWatcherCloseWhileDelivering(t *testing.T) {
	_, s, _ := watchSession(t)
	w := s.Watch(1, nil)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			s.emit(types.Event{Type: types.EventLoopStart})
		}
	}()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	w.Close() // releases the blocked sender
	wg.Wait()
	if time.Since(start) > 2*time.Second {
		t.Fatal("close did not release the delivery")
	}
}

func TestSessionCloseClosesWatchers(t *testing.T) {
	a, model, _ := channelAgent(t, nil)
	s, _ := a.NewSession(t.TempDir(), model)
	w := s.Watch(4, nil)
	s.Close()
	select {
	case _, ok := <-w.Events():
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher not closed")
	}
	if _, ok := <-s.Watch(4, nil).Events(); ok {
		t.Fatal("watching a closed session must give a closed watcher")
	}
}

func TestConcurrentResumeYieldsOneLiveSession(t *testing.T) {
	a, model, _ := channelAgent(t, nil)
	s, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()
	var regs atomic.Int64
	a.onRegisterForTest = func() { regs.Add(1) }
	const n = 32
	got := make([]*Session, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := a.ResumeSession(id)
			if err != nil {
				t.Error(err)
			}
			got[i] = r
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range got {
		if got[i] == nil || got[i] != got[0] {
			t.Fatalf("resume %d returned a different session", i)
		}
	}
	if regs.Load() != 1 {
		t.Fatalf("registered %d times", regs.Load())
	}
	got[0].Close()
}

// Only the three high-volume streaming delta types are droppable; everything
// else — including any FUTURE event type — defaults to "protected".
func TestIsControlEvent(t *testing.T) {
	droppable := map[types.EventType]bool{
		types.EventStreamTextDelta:     true,
		types.EventStreamThinkingDelta: true,
		types.EventToolArgsDelta:       true,
	}
	all := []types.EventType{
		types.EventTurnStart, types.EventTurnEnd,
		types.EventLoopStart, types.EventLoopEnd,
		types.EventStreamTextDelta, types.EventStreamTextEnd,
		types.EventStreamThinkingDelta, types.EventStreamThinkingEnd,
		types.EventToolStart, types.EventToolArgsDelta, types.EventToolCall, types.EventToolResult,
		types.EventTokens, types.EventError,
		types.EventMaxIterationsReached, types.EventFollowUpStart, types.EventReceivedPrompt,
		types.EventCompactStart, types.EventCompactEnd, types.EventStop,
	}
	for _, et := range all {
		if got, want := isControlEvent(et), !droppable[et]; got != want {
			t.Errorf("isControlEvent(%d) = %v, want %v", et, got, want)
		}
	}
}
