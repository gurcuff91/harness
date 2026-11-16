package render

import (
	"sync"
	"testing"
	"time"
)

// slowStartTerminal takes a while to enter raw mode, as a real one can.
type slowStartTerminal struct {
	captureTerminal
	mu sync.Mutex
}

func (s *slowStartTerminal) Start(func(string), func()) error {
	time.Sleep(60 * time.Millisecond)
	return nil
}
func (s *slowStartTerminal) Write(d string) { s.mu.Lock(); s.captureTerminal.Write(d); s.mu.Unlock() }

// A render can be requested BEFORE Start (adding the welcome banner to the history schedules one),
// so the timer's goroutine reads `stopped` — under t.mu — while Start, which used to write the flag
// with no lock, is still setting up the terminal. The detector flagged it on the very first frame of
// a real session. To make the window deterministic the timer is armed with its full 16 ms delay
// (as after a recent frame) and the terminal takes 60 ms to start, so the timer fires in between.
func TestStartVsAScheduledRender(t *testing.T) {
	term := &slowStartTerminal{captureTerminal: captureTerminal{cols: 80, rows: 24}}
	tui := New(term)
	tui.AddChild(&staticComponent{lines: []string{"x"}})
	tui.lastRenderAt = time.Now() // a frame was just drawn: the next one waits minRenderInterval
	tui.RequestRender(false)      // scheduled before Start, as the banner does
	if err := tui.Start(); err != nil {
		t.Fatal(err)
	}
	tui.Stop()
}
