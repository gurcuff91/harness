package tui

import (
	"sync"
	"sync/atomic"

	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// statusApp is the app= value of every report. Each report replaces its record entirely (OSC 7501),
// so it travels on all of them.
const statusApp = "harness"

// statusReporter tells the host terminal what the agent is doing, through the Program Status
// Protocol (OSC 7501): a terminal that implements it (Herdr) updates its sidebar and tab
// indicators, and may play a sound or raise a toast when a turn finishes in a tab you are not
// looking at. One that does not simply ignores the sequence, so there is nothing to detect and
// nothing appears on screen.
//
// It exists only inside the interactive TUI. ACP uses stdout as its JSON-RPC channel, and serve,
// the chat transports and -p never run the TUI, so none of them may ever emit this — which is why
// the reporter is built in exactly one place (attachTerminal) and a test pins that.
//
// Every report is exactly ONE write to the terminal the renderer draws on. The renderer writes
// each frame as one write wrapped in synchronized-output markers, and an OSC moves no cursor, so
// the two cannot interleave. Hence also: no second write path, and never fmt.Print.
//
// All methods are safe on a nil *statusReporter (they do nothing) and from any goroutine: events
// arrive on the SSE goroutine while the exit path runs on another.
type statusReporter struct {
	mu     sync.Mutex
	write  func(string)
	last   string // the last state written; "" before the first
	closed bool   // Clear was called: the record is gone and must stay gone

	// The terminal title (OSC 0) travels through the same reporter, so it has the same rules for
	// free: one write per sequence, on the renderer's own terminal, ordered with the status
	// writes, nil-safe, and silent once Clear has run.
	title  string // the last title written (already sanitized)
	titled bool   // a title was written: only then is there one to restore on exit
}

// newStatusReporter returns a reporter that emits through write.
func newStatusReporter(write func(string)) *statusReporter {
	return &statusReporter{write: write}
}

// Set reports state, unless it is the state last reported: loop_start fires on every ReAct
// iteration and the protocol has no heartbeat, so a repeat would only be noise.
func (r *statusReporter) Set(state string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setLocked(state)
}

// IdleIfWorking reports idle only when a turn is live (the last state is working). It is what an
// event stream that ends reports: a turn that was running will never finish on this stream, so it
// must not stay "working" — but a finished turn's done/error is a result, and survives a session
// switch.
func (r *statusReporter) IdleIfWorking() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == ansi.StatusWorking {
		r.setLocked(ansi.StatusIdle)
	}
}

// SetTitle publishes the session's name as the terminal title (OSC 0) — what Herdr shows as the
// name of an unnamed tab and in its agents list, and what any terminal shows on its tab or window.
//
// The format is the session's own name, bare. A prefix such as "Harness · " would repeat what the
// agents list already says (it shows the program beside the title), and Herdr's agents mod throws
// away a title that equals the program name, looks like a path or contains "@": a session name is
// none of those. (A session literally named "harness" would be dropped by it; nothing to do.)
//
// The name is sanitized here, not by the callers: it can come from the user's own prompt, and an
// unsanitized BEL or ESC would end the sequence early or inject escapes (see ansi.SanitizeTitle).
// A repeat of the title last written is not written again. A name that sanitizes to nothing is
// not published as a title until there is a real one to replace.
func (r *statusReporter) SetTitle(name string) {
	if r == nil {
		return
	}
	title := ansi.SanitizeTitle(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || (r.titled && title == r.title) || (!r.titled && title == "") {
		return
	}
	r.write(ansi.SetTitle(title))
	r.title = title
	r.titled = true
}

// Clear drops this program's record, on exit. It is terminal: afterwards nothing is reported, so a
// late event (a stream goroutine waking up after the user quit) cannot bring the record back and
// leave it stale in the terminal's sidebar.
func (r *statusReporter) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	// Give the title back first, so that the clear stays the last thing reported. An empty title is
	// the simple restore: the terminal falls back to its own (the shell's prompt sets it again as
	// soon as the shell is back). The XTWINOPS title stack (CSI 22;0 t on start, CSI 23;0 t on
	// exit) would restore the exact previous title, but only some terminals implement it, and an
	// unsupported one would be left with a title nobody set. Only a title this reporter set is
	// given back: a session that never published one leaves the terminal's title alone.
	if r.titled && r.title != "" {
		r.write(ansi.SetTitle(""))
	}
	r.write(ansi.ProgramStatus(ansi.StatusClear, statusApp))
	r.last = ansi.StatusClear
	r.closed = true
}

// setLocked writes state if it changed. The write happens under the mutex so that the order of the
// writes is the order of the calls.
func (r *statusReporter) setLocked(state string) {
	if r.closed || state == r.last {
		return
	}
	r.write(ansi.ProgramStatus(state, statusApp))
	r.last = state
}

// turnOutcome is how the turn in flight is going, as far as its final report is concerned. It is
// what lets turn_end — which carries no outcome of its own — say done, idle or error.
type turnOutcome int32

const (
	outcomeOK      turnOutcome = iota // nothing went wrong (the default)
	outcomeStopped                    // the user stopped the turn
	outcomeError                      // the turn (or a command) failed
)

// outcomeFlag is a turnOutcome that any goroutine may read and write: events arrive on the SSE
// goroutine of whichever stream is current, and a stream being replaced overlaps the new one.
type outcomeFlag struct{ v atomic.Int32 }

func (f *outcomeFlag) set(o turnOutcome) { f.v.Store(int32(o)) }
func (f *outcomeFlag) get() turnOutcome  { return turnOutcome(f.v.Load()) }

// reportTurnEnd is the state turn_end reports, and whether it reports anything.
//
// With work still queued the agent is not finished — the next follow_up_start continues the same
// run of work — so nothing is reported and the state stays working; that is also what the spinner
// does. Otherwise the outcome decides: stopped is idle (not done: nothing finished), error is
// error, and only an untroubled turn is done. The flag is read, not reset: turn_start and
// loop_start do that, which is what lets a failed auto-compaction (an error event, then the turn
// carries on) still end in done.
func reportTurnEnd(o turnOutcome, queued int) (state string, ok bool) {
	switch {
	case queued > 0:
		return "", false
	case o == outcomeStopped:
		return ansi.StatusIdle, true
	case o == outcomeError:
		return ansi.StatusError, true
	default:
		return ansi.StatusDone, true
	}
}
