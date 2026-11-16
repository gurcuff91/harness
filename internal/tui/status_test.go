package tui

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/components"
	"github.com/gurcuff91/harness/internal/tui/term"
)

// recTerminal is a terminal that records every Write, in order, as the bytes the host terminal
// would receive. It replaces the no-op mockTerminal for the status tests.
type recTerminal struct {
	mockTerminal
	mu     sync.Mutex
	writes []string
}

func newRecTerminal() *recTerminal {
	return &recTerminal{mockTerminal: mockTerminal{cols: 80, rows: 24}}
}

// Stop leaves a marker in the write log so a test can see WHERE in the sequence the renderer
// stopped (the terminal is back to normal from that point).
func (r *recTerminal) Stop() { r.Write("<stop>") }

func (r *recTerminal) Write(s string) { r.mu.Lock(); r.writes = append(r.writes, s); r.mu.Unlock() }

func (r *recTerminal) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writes...)
}

// reports is the sequence of states written as OSC 7501, in order. Anything else written to the
// terminal (frames) is ignored here; TestAReportIsExactlyOneWriteOfExactlyTheOSC checks the rest.
func (r *recTerminal) reports() []string {
	var out []string
	for _, w := range r.all() {
		if !strings.Contains(w, "\x1b]7501;") {
			continue
		}
		const pre = "\x1b]7501;state="
		i := strings.Index(w, pre)
		rest := w[i+len(pre):]
		out = append(out, rest[:strings.Index(rest, ":")])
	}
	return out
}

// statusTUI is a TUI wired like the real one (attachTerminal) over a recording terminal.
func statusTUI(t *testing.T) (*TUI, *recTerminal) {
	t.Helper()
	rec := newRecTerminal()
	tu := New(nil)
	tu.attachTerminal(rec)
	tu.history = components.NewHistory()
	tu.spinner = components.NewSpinner(tu.tui, "")
	tu.info = components.NewTruncatedText("", 0)
	tu.footer = components.NewTruncatedText("", 0)
	return tu, rec
}

// play feeds events through consumeEvents and returns once the stream is consumed. The channel is
// closed afterwards, so the end of a stream is part of what is played unless closed is false.
func play(t *testing.T, tu *TUI, evs ...client.Event) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := feed(evs)
	close(ch)
	tu.consumeEvents(ctx, ch)
}

func ev(types ...string) []client.Event {
	var out []client.Event
	for _, ty := range types {
		out = append(out, client.Event{Type: ty})
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("reported %v, want %v", got, want)
	}
}

// ── the protocol bytes ──────────────────────────────────────────────────────

func TestProgramStatusIsTheExactOSC(t *testing.T) {
	for state, want := range map[string]string{
		ansi.StatusWorking: "\x1b]7501;state=working:app=harness\x1b\\",
		ansi.StatusIdle:    "\x1b]7501;state=idle:app=harness\x1b\\",
		ansi.StatusDone:    "\x1b]7501;state=done:app=harness\x1b\\",
		ansi.StatusError:   "\x1b]7501;state=error:app=harness\x1b\\",
		ansi.StatusClear:   "\x1b]7501;state=clear:app=harness\x1b\\",
	} {
		if got := ansi.ProgramStatus(state, "harness"); got != want {
			t.Errorf("ProgramStatus(%q) = %q, want %q", state, got, want)
		}
	}
	// the names are the protocol's own
	for c, want := range map[string]string{ansi.StatusWorking: "working", ansi.StatusIdle: "idle", ansi.StatusDone: "done", ansi.StatusError: "error", ansi.StatusClear: "clear"} {
		if c != want {
			t.Errorf("state constant %q, want %q", c, want)
		}
	}
	if got := ansi.ProgramStatus("working", "other"); !strings.Contains(got, ":app=other") {
		t.Errorf("the app is a parameter: %q", got)
	}
}

// ── the mapping, one row at a time ──────────────────────────────────────────

func TestTheMappingRowByRow(t *testing.T) {
	for _, c := range []struct {
		name   string
		events []client.Event
		queued int // queueCount when the events start
		want   []string
	}{
		{"turn_start", ev("turn_start"), 0, []string{"working"}},
		{"received_prompt", ev("received_prompt"), 0, []string{"working"}},
		{"follow_up_start", ev("follow_up_start"), 0, []string{"working"}},
		{"loop_start", ev("loop_start"), 0, []string{"working"}},
		{"error outside a turn (a failed /compact)", ev("error"), 0, []string{"error"}},
		{"turn_end, ok", ev("turn_start", "turn_end"), 0, []string{"working", "done"}},
		{"turn_end, stopped", ev("turn_start", "stop", "turn_end"), 0, []string{"working", "idle"}},
		{"turn_end, error", ev("turn_start", "error", "turn_end"), 0, []string{"working", "error", "error"[:0] + "error"}},
		{"turn_end with queued work reports nothing", ev("turn_start", "turn_end"), 1, []string{"working"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tu, rec := statusTUI(t)
			tu.queueCount = c.queued
			// the stream ends open here: the end of a stream has its own rows below
			ctx, cancel := context.WithCancel(context.Background())
			ch := feed(c.events)
			done := make(chan struct{})
			go func() { tu.consumeEvents(ctx, ch); close(done) }()
			deadline := time.Now().Add(3 * time.Second)
			for len(ch) > 0 && time.Now().Before(deadline) {
				time.Sleep(2 * time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
			cancel()
			<-done
			// "error" then turn_end(error) is the same state twice: the dedupe makes it one write.
			want := c.want
			if c.name == "turn_end, error" {
				want = []string{"working", "error"}
			}
			eq(t, rec.reports(), want)
		})
	}
}

// ── the turn outcome ────────────────────────────────────────────────────────

func TestOutcomeSequences(t *testing.T) {
	for _, c := range []struct {
		name   string
		events []client.Event
		want   []string
	}{
		{"a normal turn: working then done", ev("turn_start", "loop_start", "text", "loop_end", "turn_end"), []string{"working", "done"}},
		{"stop then turn_end is idle, not done", ev("turn_start", "loop_start", "stop", "turn_end"), []string{"working", "idle"}},
		{"error then turn_end is error, not done", ev("turn_start", "loop_start", "error", "turn_end"), []string{"working", "error"}},
		{"a failed auto-compaction that continues: back to working, ends done",
			ev("turn_start", "error", "loop_start", "text", "turn_end"), []string{"working", "error", "working", "done"}},
		{"a retry notice is not a failure", ev("turn_start", "retry", "turn_end"), []string{"working", "done"}},
		{"turn_end does not reset the outcome", ev("turn_start", "error", "turn_end", "turn_end"), []string{"working", "error"}},
		{"the next turn starts clean after an error", ev("turn_start", "error", "turn_end", "turn_start", "turn_end"), []string{"working", "error", "working", "done"}},
		{"the next turn starts clean after a stop", ev("turn_start", "stop", "turn_end", "turn_start", "turn_end"), []string{"working", "idle", "working", "done"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tu, rec := statusTUI(t)
			ctx, cancel := context.WithCancel(context.Background())
			ch := feed(c.events)
			done := make(chan struct{})
			go func() { tu.consumeEvents(ctx, ch); close(done) }()
			for len(ch) > 0 {
				time.Sleep(2 * time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
			cancel()
			<-done
			eq(t, rec.reports(), c.want)
		})
	}
}

func TestAQueuedFollowUpNeverShowsDoneBetweenTheTwoTurns(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.queueCount = 1 // one prompt waiting behind the running turn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan client.Event, 16)
	go tu.consumeEvents(ctx, ch)
	send := func(typ string) { ch <- client.Event{Type: typ}; time.Sleep(15 * time.Millisecond) }

	send("turn_start")
	send("turn_end") // queueCount is 1: the agent is not finished
	if got := rec.reports(); strings.Join(got, ",") != "working" {
		t.Fatalf("between the turns the terminal was told %v, want it to stay on working", got)
	}
	send("follow_up_start") // dequeues: queueCount 1 -> 0
	send("turn_start")
	send("turn_end")
	eq(t, rec.reports(), []string{"working", "done"})
}

// ── dedupe, one write, the bytes ────────────────────────────────────────────

func TestTwoConsecutiveIdenticalStatesAreOneWrite(t *testing.T) {
	tu, rec := statusTUI(t)
	// loop_start fires on every ReAct iteration. The stream is left open: its end is a report of
	// its own (idle), covered separately.
	ctx, cancel := context.WithCancel(context.Background())
	ch := feed(ev("turn_start", "loop_start", "loop_start", "loop_start", "received_prompt", "follow_up_start"))
	done := make(chan struct{})
	go func() { tu.consumeEvents(ctx, ch); close(done) }()
	for len(ch) > 0 {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if n := len(rec.reports()); n != 1 {
		t.Errorf("%d writes for six working triggers, want 1: %v", n, rec.reports())
	}
	// ...but the same state again after a different one is written again
	tu2, rec2 := statusTUI(t)
	tu2.status.Set(ansi.StatusWorking)
	tu2.status.Set(ansi.StatusDone)
	tu2.status.Set(ansi.StatusWorking)
	eq(t, rec2.reports(), []string{"working", "done", "working"})
}

func TestAReportIsExactlyOneWriteOfExactlyTheOSC(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.status.Set(ansi.StatusWorking)
	tu.status.Set(ansi.StatusError)
	tu.status.Clear()
	want := []string{
		"\x1b]7501;state=working:app=harness\x1b\\",
		"\x1b]7501;state=error:app=harness\x1b\\",
		"\x1b]7501;state=clear:app=harness\x1b\\",
	}
	got := rec.all()
	if len(got) != len(want) {
		t.Fatalf("%d writes, want %d (one per report): %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("write %d = %q, want %q: a report must be a single Write carrying nothing else", i, got[i], want[i])
		}
		if !strings.Contains(got[i], "app=harness") {
			t.Errorf("write %d lacks app=harness", i)
		}
	}
}

func TestEveryReportCarriesAppHarnessAndNothingVisible(t *testing.T) {
	tu, rec := statusTUI(t)
	play(t, tu, ev("turn_start", "error", "loop_start", "turn_end")...)
	tu.status.Clear()
	for _, w := range rec.all() {
		if !strings.Contains(w, "7501") {
			continue
		}
		if !strings.Contains(w, ":app=harness") {
			t.Errorf("report %q lacks app=harness", w)
		}
		// the whole write is the OSC: strip it and nothing may remain
		if rest := strings.TrimSuffix(strings.TrimPrefix(w, "\x1b]7501;"), "\x1b\\"); strings.ContainsAny(rest, "\x1b\r\n\x07") {
			t.Errorf("report %q carries more than the sequence", w)
		}
	}
}

// ── the end of a stream ─────────────────────────────────────────────────────

func TestAStreamThatEndsMidTurnReportsIdle(t *testing.T) {
	tu, rec := statusTUI(t)
	play(t, tu, ev("turn_start", "loop_start")...) // the channel closes with the turn live
	eq(t, rec.reports(), []string{"working", "idle"})
}

func TestAStreamThatEndsAfterAFinishedTurnKeepsItsResult(t *testing.T) {
	for _, c := range []struct {
		events []string
		want   []string
	}{
		{[]string{"turn_start", "turn_end"}, []string{"working", "done"}},
		{[]string{"turn_start", "error", "turn_end"}, []string{"working", "error"}},
		{[]string{"turn_start", "stop", "turn_end"}, []string{"working", "idle"}},
	} {
		tu, rec := statusTUI(t)
		play(t, tu, ev(c.events...)...)
		eq(t, rec.reports(), c.want) // no extra idle after the result
	}
}

func TestACancelledStreamDoesNotReportItself(t *testing.T) {
	// Cancelling is a session switch or shutdown: stopSSE reports at the moment it cancels, and
	// the late-waking consumer must not report again — it could overwrite the NEW stream's state.
	tu, rec := statusTUI(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan client.Event, 4)
	done := make(chan struct{})
	go func() { tu.consumeEvents(ctx, ch); close(done) }()
	ch <- client.Event{Type: "turn_start"}
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	eq(t, rec.reports(), []string{"working"})
}

func TestSwitchingSessionMidTurnLeavesNoStuckWorking(t *testing.T) {
	tu, rec := statusTUI(t)
	cancelled := false
	tu.sseCancel = func() { cancelled = true }
	tu.status.Set(ansi.StatusWorking) // a turn is running on the old stream
	tu.stopSSE()                      // resume / fork / new: the old stream goes away
	if !cancelled || tu.sseCancel != nil {
		t.Fatal("stopSSE must cancel and clear the stream")
	}
	eq(t, rec.reports(), []string{"working", "idle"})

	// the new session then reports on its own and is not overwritten by the old consumer
	tu.status.Set(ansi.StatusWorking)
	tu.reportStreamLost(canceledCtx()) // the OLD consumer waking up late
	eq(t, rec.reports(), []string{"working", "idle", "working"})
}

func TestSwitchingSessionAfterAResultKeepsTheResult(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.status.Set(ansi.StatusWorking)
	tu.status.Set(ansi.StatusDone)
	tu.stopSSE()
	eq(t, rec.reports(), []string{"working", "done"})
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// ── exit ────────────────────────────────────────────────────────────────────

func TestClearDropsTheRecordOnceAndStaysCleared(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.status.Set(ansi.StatusWorking)
	tu.status.Clear()
	tu.status.Clear()                 // idempotent
	tu.status.Set(ansi.StatusWorking) // a late event after the user quit
	tu.status.IdleIfWorking()         // a late stream consumer
	tu.status.Set(ansi.StatusError)
	eq(t, rec.reports(), []string{"working", "clear"})
}

func TestClearIsReportedEvenWhenNothingWasReportedBefore(t *testing.T) {
	// a session that never ran a turn must still leave no record behind (a stale one from an
	// earlier run, for instance)
	tu, rec := statusTUI(t)
	tu.status.Clear()
	eq(t, rec.reports(), []string{"clear"})
}

func TestANilReporterIsHarmless(t *testing.T) {
	var r *statusReporter
	r.Set(ansi.StatusWorking)
	r.IdleIfWorking()
	r.Clear()
	tu := New(nil) // buildUI has not run: status is nil
	tu.stopSSE()
	tu.reportStreamLost(context.Background())
}

func TestReporterIsSafeUnderConcurrentUse(t *testing.T) {
	tu, rec := statusTUI(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				switch (i + j) % 4 {
				case 0:
					tu.status.Set(ansi.StatusWorking)
				case 1:
					tu.status.Set(ansi.StatusDone)
				case 2:
					tu.status.IdleIfWorking()
				case 3:
					if j == 199 {
						tu.status.Clear()
					}
				}
			}
		}(i)
	}
	wg.Wait()
	// whatever the interleaving, no state repeats back to back and nothing follows the clear
	rs := rec.reports()
	for i := 1; i < len(rs); i++ {
		if rs[i] == rs[i-1] {
			t.Fatalf("a repeat at %d: %v", i, rs[i-1:i+1])
		}
	}
	if len(rs) == 0 || rs[len(rs)-1] != "clear" {
		t.Errorf("the last report is %v, want clear: nothing may follow it", rs[len(rs)-1:])
	}
}

// ── the real Run: exit ──────────────────────────────────────────────────────

// runTUI runs the REAL Run (server, buildUI, autoConnect, render loop, exit) over a recording
// terminal, quits it, and returns what the terminal received and what was printed on stdout.
func runTUI(t *testing.T, afterStart func(tu *TUI, rec *recTerminal)) (writes []string, stdout string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()

	rec := newRecTerminal()
	tu := New(a)
	tu.newTerminal = func() term.Terminal { return rec }

	// Run prints its farewell with fmt.Printf (to os.Stdout, read at call time): capture it.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	out := make(chan string)
	go func() { b, _ := io.ReadAll(r); out <- string(b) }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tu.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	// Wait on the recording terminal (mutex-guarded), never on a TUI field Run is still writing.
	for len(rec.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the TUI never started drawing")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if afterStart != nil {
		afterStart(tu, rec)
	}
	tu.quit()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after quit")
	}
	cancel()
	w.Close()
	os.Stdout = orig
	return rec.all(), <-out
}

func TestRunClearsTheRecordAfterTheRendererStopsAndBeforeAnythingElse(t *testing.T) {
	writes, stdout := runTUI(t, func(tu *TUI, rec *recTerminal) {
		tu.status.Set(ansi.StatusWorking)
		tu.status.Set(ansi.StatusDone) // a result is on the record when the user quits
	})
	var reports []string
	stopAt, clearAt := -1, -1
	for i, w := range writes {
		if w == "<stop>" {
			stopAt = i
		}
		if strings.Contains(w, "state=clear") {
			clearAt = i
		}
		if strings.Contains(w, "\x1b]7501;") {
			reports = append(reports, w)
		}
	}
	if clearAt < 0 {
		t.Fatalf("Run never cleared the record; reports: %q", reports)
	}
	if stopAt < 0 || clearAt < stopAt {
		t.Errorf("clear at %d, renderer stopped at %d: the record is dropped only once the terminal is back to normal", clearAt, stopAt)
	}
	if clearAt != len(writes)-1 {
		t.Errorf("clear is write %d of %d: nothing may be drawn after it", clearAt, len(writes))
	}
	if writes[clearAt] != "\x1b]7501;state=clear:app=harness\x1b\\" {
		t.Errorf("the clear is %q", writes[clearAt])
	}
	// the farewell is still printed, and the OSC never reaches stdout through it
	if !strings.Contains(stdout, "Goodbye") || strings.Contains(stdout, "7501") {
		t.Errorf("farewell output %q", stdout)
	}
}

func TestRunClearsEvenIfNothingEverRan(t *testing.T) {
	writes, _ := runTUI(t, nil)
	n := 0
	for _, w := range writes {
		if strings.Contains(w, "state=clear") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d clear reports, want exactly 1", n)
	}
	for _, w := range writes {
		if strings.Contains(w, "state=working") || strings.Contains(w, "state=done") {
			t.Errorf("a session that ran no turn reported %q", w)
		}
	}
}

// ── session switches, through the real server ───────────────────────────────

// connectedTUI is a TUI on a real server session (the fallback fixture's), with the status
// reporter attached to a recording terminal. autoConnect's own stream is replaced by none: the
// tests below drive the status directly, as a running turn would have left it.
func connectedTUI(t *testing.T) (*TUI, *recTerminal, *fallbackFixture) {
	t.Helper()
	f := newFallbackFixture(t)
	rec := newRecTerminal()
	tu := New(nil)
	tu.attachTerminal(rec)
	tu.history = components.NewHistory()
	tu.spinner = components.NewSpinner(tu.tui, "")
	tu.info = components.NewTruncatedText("", 0)
	tu.footer = components.NewTruncatedText("", 0)
	tu.client = f.c
	tu.baseCtx = context.Background()
	tu.resumeID = f.sessionID
	tu.autoConnect(context.Background())
	if tu.sessionID != f.sessionID {
		t.Fatalf("setup: on %q, want %q", tu.sessionID, f.sessionID)
	}
	t.Cleanup(func() {
		if tu.sseCancel != nil {
			tu.sseCancel()
		}
	})
	return tu, rec, f
}

func TestForkMidTurnLeavesNoStuckWorking(t *testing.T) {
	tu, rec, _ := connectedTUI(t)
	tu.status.Set(ansi.StatusWorking) // a turn is running on the parent
	tu.cmdFork()
	if tu.sessionID == "" {
		t.Fatal("the fork did not switch the session")
	}
	eq(t, rec.reports(), []string{"working", "idle"})
}

func TestResumeInPlaceMidTurnLeavesNoStuckWorking(t *testing.T) {
	tu, rec, f := connectedTUI(t)
	tu.status.Set(ansi.StatusWorking)
	tu.resumeInPlace(f.sessionID)
	eq(t, rec.reports(), []string{"working", "idle"})
}

func TestNewSessionInPlaceMidTurnLeavesNoStuckWorking(t *testing.T) {
	tu, rec, _ := connectedTUI(t)
	tu.status.Set(ansi.StatusWorking)
	tu.newSessionInPlace()
	eq(t, rec.reports(), []string{"working", "idle"})
}

func TestASwitchAfterAFinishedTurnKeepsItsResult(t *testing.T) {
	tu, rec, _ := connectedTUI(t)
	tu.status.Set(ansi.StatusWorking)
	tu.status.Set(ansi.StatusDone)
	tu.cmdFork()
	eq(t, rec.reports(), []string{"working", "done"})
}

// When the stream cannot even be opened (the session is gone on the server) the turn that was
// working will never end, so the terminal must not stay on working.
func TestAStreamThatCannotBeOpenedReportsIdle(t *testing.T) {
	tu, rec, _ := connectedTUI(t)
	tu.stopSSE()
	// autoConnect's stream goroutine reads t.sessionID: let it finish before changing the field.
	time.Sleep(200 * time.Millisecond)
	tu.status.Set(ansi.StatusWorking)
	tu.sessionID = "no-such-session"
	tu.streamEvents(context.Background())
	eq(t, rec.reports(), []string{"working", "idle"})
}
