package server

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

// newTestProxy builds a SessionProxy without a real *agent.Session — broadcast
// and isControlEvent don't touch p.session, only p.clients, so this is safe
// for testing the fan-out/backpressure behavior in isolation. verbose
// defaults to false (the zero value) — the safer default, and what the TUI's
// in-process server actually uses.
func newTestProxy() *SessionProxy {
	return &SessionProxy{clients: make(map[chan<- []byte]struct{})}
}

// captureLog redirects the standard logger's output during fn, so tests can
// assert whether logx (which writes through log.Print) produced anything.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldW)
		log.SetFlags(oldF)
	}()
	fn()
	return strings.TrimRight(buf.String(), "\n")
}

// TestIsControlEvent locks in the denylist: only the three high-volume
// streaming delta types are droppable; everything else (including any FUTURE
// event type not listed here) defaults to "protected" — the safer failure
// mode for a type this function doesn't yet know about.
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
		types.EventMaxTurnsReached, types.EventFollowUpStart, types.EventReceivedPrompt,
		types.EventCompactStart, types.EventCompactEnd, types.EventStop,
	}
	for _, et := range all {
		want := !droppable[et]
		if got := isControlEvent(et); got != want {
			t.Errorf("isControlEvent(%d) = %v, want %v", et, got, want)
		}
	}
}

// TestBroadcastDropsStreamingDeltaOnFullChannel verifies a high-volume
// streaming delta (thinking/text/tool_args) is dropped immediately — never
// blocks broadcast — when the client's channel is full. This is the
// deliberate trade-off: s.emit calls broadcast synchronously from the agent's
// ReAct loop, so this path must never stall the turn.
func TestBroadcastDropsStreamingDeltaOnFullChannel(t *testing.T) {
	p := newTestProxy()
	ch := make(chan []byte, 1) // capacity 1, easy to fill
	p.addClient(ch)

	// Fill the channel with one delta so the next send has nowhere to go.
	p.broadcast(types.Event{Type: types.EventStreamTextDelta, Delta: "first"})
	if len(ch) != 1 {
		t.Fatalf("setup: expected channel filled with 1 item, got %d", len(ch))
	}

	start := time.Now()
	p.broadcast(types.Event{Type: types.EventStreamTextDelta, Delta: "second — should be dropped"})
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("broadcasting a streaming delta into a full channel took %v — should return immediately (non-blocking)", elapsed)
	}
	// The channel must still hold only the FIRST delta — the second was dropped,
	// not queued behind it.
	got := <-ch
	if !strings.Contains(string(got), "first") {
		t.Errorf("channel should still hold the first delta, got: %s", got)
	}
	select {
	case extra := <-ch:
		t.Errorf("channel should be empty after draining the first item, got extra: %s", extra)
	default:
	}
}

// TestBroadcastRetriesControlEventOnFullChannel verifies a control event
// (e.g. turn_end) is NOT dropped immediately when the channel is momentarily
// full — it waits (up to controlBroadcastTimeout) for room, so a client
// that's briefly behind (draining a backlog of deltas) still gets the signal
// that tells it the turn ended / was stopped. This is the fix for the
// reported freeze: losing turn_end/stop left the TUI spinner stuck forever
// with no way to know the agent was actually done.
func TestBroadcastRetriesControlEventOnFullChannel(t *testing.T) {
	p := newTestProxy()
	ch := make(chan []byte, 1)
	p.addClient(ch)

	// Fill the channel.
	p.broadcast(types.Event{Type: types.EventStreamTextDelta, Delta: "filler"})
	if len(ch) != 1 {
		t.Fatalf("setup: expected channel filled, got %d", len(ch))
	}

	// Drain it concurrently, after a short delay — simulating a consumer that's
	// momentarily behind but does catch up (the realistic case: a render
	// backlog draining), not permanently stuck.
	drained := make(chan []byte, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		drained <- <-ch
	}()

	start := time.Now()
	p.broadcast(types.Event{Type: types.EventTurnEnd})
	elapsed := time.Since(start)

	if elapsed < 15*time.Millisecond {
		t.Errorf("control event delivered too fast (%v) — test setup issue: the drain goroutine should have made it wait", elapsed)
	}
	if elapsed > controlBroadcastTimeout {
		t.Errorf("control event broadcast took %v — should have found room well within controlBroadcastTimeout (%v)", elapsed, controlBroadcastTimeout)
	}

	<-drained // consume the filler this goroutine drained, keep it tidy

	select {
	case got := <-ch:
		if !strings.Contains(string(got), "turn_end") {
			t.Errorf("expected the turn_end event to have been delivered, got: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("turn_end was never delivered to the channel")
	}
}

// TestBroadcastDropsControlEventAfterTimeoutOnDeadClient verifies the last-
// resort behavior: if a client's channel NEVER drains (truly stuck, not just
// momentarily behind), broadcast eventually gives up after
// controlBroadcastTimeout instead of blocking forever — a dead client must
// never be able to hang the agent's ReAct loop, which calls this
// synchronously via s.emit.
func TestBroadcastDropsControlEventAfterTimeoutOnDeadClient(t *testing.T) {
	p := newTestProxy()
	ch := make(chan []byte, 1)
	p.addClient(ch)
	p.broadcast(types.Event{Type: types.EventStreamTextDelta, Delta: "filler"}) // fill it; nobody ever drains

	start := time.Now()
	p.broadcast(types.Event{Type: types.EventStop})
	elapsed := time.Since(start)

	if elapsed < controlBroadcastTimeout {
		t.Errorf("broadcast returned in %v, want it to wait out controlBroadcastTimeout (%v) before giving up", elapsed, controlBroadcastTimeout)
	}
	if elapsed > controlBroadcastTimeout+200*time.Millisecond {
		t.Errorf("broadcast took %v — much longer than controlBroadcastTimeout (%v), did it block instead of timing out?", elapsed, controlBroadcastTimeout)
	}
}

// TestBroadcastSilentWhenNotVerbose is the regression test for the TUI-corruption
// hazard: the TUI's in-process server always runs with Verbose: false because it
// shares stdout/stderr with the raw-mode terminal renderer — ANY unconditional
// log line from a background goroutine (like the agent's own event-emitting
// goroutine, which is what calls broadcast) would corrupt the display. A dropped
// control event (dead client, see the timeout test above) must NOT log anything
// when verbose is off.
func TestBroadcastSilentWhenNotVerbose(t *testing.T) {
	p := newTestProxy() // verbose defaults to false
	ch := make(chan []byte, 1)
	p.addClient(ch)
	p.broadcast(types.Event{Type: types.EventStreamTextDelta, Delta: "filler"}) // fill; never drained

	out := captureLog(t, func() {
		p.broadcast(types.Event{Type: types.EventStop}) // dead client -> would-be-dropped control event
	})

	if out != "" {
		t.Errorf("broadcast logged with verbose=false — this would corrupt the TUI's raw-mode render: %q", out)
	}
}

// TestBroadcastLogsWhenVerbose verifies the OTHER side: standalone `harness
// serve` / Telegram run with Verbose: true and DO want a dropped control event
// visible (it's a real, actionable signal there — no raw-mode terminal to
// corrupt).
func TestBroadcastLogsWhenVerbose(t *testing.T) {
	p := newTestProxy()
	p.verbose = true
	ch := make(chan []byte, 1)
	p.addClient(ch)
	p.broadcast(types.Event{Type: types.EventStreamTextDelta, Delta: "filler"})

	out := captureLog(t, func() {
		p.broadcast(types.Event{Type: types.EventStop})
	})

	if !strings.Contains(out, "control_event_dropped") {
		t.Errorf("expected a control_event_dropped warning with verbose=true, got: %q", out)
	}
}
