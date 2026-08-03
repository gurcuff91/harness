package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/components"
	"github.com/gurcuff91/harness/internal/tui/render"
)

// feed loads a slice of events into a buffered channel and returns it (closed
// or open per caller need). Kept as a helper so each test reads as a list of
// typed client.Event values, mirroring the SSE stream the TUI consumes.
func feed(events []client.Event) chan client.Event {
	ch := make(chan client.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	return ch
}

// mockTerminal captures writes and reports a fixed size.
type mockTerminal struct {
	cols, rows int
}

func (m *mockTerminal) Start(func(string), func()) error { return nil }
func (m *mockTerminal) Stop()                            {}
func (m *mockTerminal) Write(string)                     {}
func (m *mockTerminal) Columns() int                     { return m.cols }
func (m *mockTerminal) Rows() int                        { return m.rows }
func (m *mockTerminal) MoveBy(int)                       {}
func (m *mockTerminal) HideCursor()                      {}
func (m *mockTerminal) ShowCursor()                      {}
func (m *mockTerminal) ClearLine()                       {}
func (m *mockTerminal) ClearFromCursor()                 {}
func (m *mockTerminal) ClearScreen()                     {}

// newTestTUI builds a minimal TUI good enough to drive consumeEvents without
// the SSE/HTTP server stack. History rendering is exercised through the real
// history; other UI pieces (spinner, editor, footer) are stubbed.
func newTestTUIForEvents() *TUI {
	term := &mockTerminal{cols: 80, rows: 24}
	t := New(nil)
	t.tui = render.New(term)
	t.history = components.NewHistory()
	t.spinner = components.NewSpinner(t.tui, "")
	t.info = components.NewTruncatedText("", 0)
	t.footer = components.NewTruncatedText("", 0)
	return t
}

// blockSummary returns a short label describing each block in history (kind +
// a snippet of its text). Used to assert chronology without depending on the
// full rendered ANSI output.
func blockSummary(t *TUI) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, t.history.Len())
	for _, b := range t.history.Blocks() {
		switch v := b.(type) {
		case *components.RawBlock:
			txt := v.Text()
			snippet := strings.ReplaceAll(strings.ReplaceAll(txt, "\n", "\\n"), "\t", "\\t")
			if len(snippet) > 60 {
				snippet = snippet[:60] + "..."
			}
			out = append(out, "raw:"+snippet)
		case *components.Spacer:
			out = append(out, "spacer")
		case *components.Markdown:
			out = append(out, "md:"+v.Source())
		default:
			out = append(out, "?")
		}
	}
	return out
}

// TestThinkingAfterToolCreatesNewBlock reproduces the regression where a
// thinking delta arriving after one or more tool calls rewrote the FIRST
// thinking block in place — even though chronologically the new reasoning
// belongs at the END of the history, after the tool calls. The user-visible
// symptom was the streaming text "jumping" back up to a position above the
// tool calls.
func TestThinkingAfterToolCreatesNewBlock(t *testing.T) {
	tui := newTestTUIForEvents()
	events := []client.Event{
		{Type: "turn_start"},
		{Type: "thinking", Delta: "First reasoning "},
		{Type: "thinking", Delta: "block."},
		{Type: "thinking_end"},
		{Type: "tool_start", ToolID: "t1", ToolName: "Bash"},
		{Type: "tool_args", ToolID: "t1", Delta: `{"command":"ls"}`},
		{Type: "tool_call", ToolID: "t1", ToolArgs: `{"command":"ls"}`},
		{Type: "tool_result", ToolID: "t1", Output: "file.txt", IsError: false},
		{Type: "thinking", Delta: "Second reasoning "},
		{Type: "thinking", Delta: "after tool."},
		{Type: "thinking_end"},
	}

	// Drain via consumeEvents on its own goroutine and close the channel when done.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := feed(events)
	close(ch)
	tui.consumeEvents(ctx, ch)

	summary := blockSummary(tui)

	// Locate the first and second thinking blocks.
	var firstIdx, secondIdx = -1, -1
	for i, s := range summary {
		if strings.HasPrefix(s, "raw:") {
			if strings.Contains(s, "First reasoning") && firstIdx == -1 {
				firstIdx = i
			}
			if strings.Contains(s, "Second reasoning") {
				secondIdx = i
			}
		}
	}
	if firstIdx == -1 {
		t.Fatalf("first thinking block not found: %v", summary)
	}
	if secondIdx == -1 {
		t.Fatalf("second thinking block not found: %v", summary)
	}

	// The second thinking block must come AFTER the tool blocks.
	var toolIdx = -1
	for i, s := range summary {
		if strings.HasPrefix(s, "raw:") && strings.Contains(s, "Bash") {
			toolIdx = i
			break
		}
	}
	if toolIdx == -1 {
		t.Fatalf("tool block not found: %v", summary)
	}
	if secondIdx <= toolIdx {
		t.Errorf("second thinking block is at index %d but tool block is at %d — new reasoning was inserted in the wrong position: %v",
			secondIdx, toolIdx, summary)
	}

	// And it must NOT have rewritten the first thinking block: first must
	// still contain "First reasoning", not "Second reasoning".
	if !strings.Contains(summary[firstIdx], "First reasoning") {
		t.Errorf("first thinking block was rewritten by the second reasoning stream: %v", summary)
	}
}

// TestThinkingAfterMultipleToolsKeepsChronology reproduces the original
// user-reported bug: with several tool calls in a row, a new thinking
// fragment arriving after them used to (inadvertently) edit the FIRST
// thinking block in place. After the fix, the new fragment becomes its own
// block at the very end of the history.
func TestThinkingAfterMultipleToolsKeepsChronology(t *testing.T) {
	tui := newTestTUIForEvents()
	events := []client.Event{
		{Type: "turn_start"},
		{Type: "thinking", Delta: "Plan "},
		{Type: "thinking", Delta: "before tools."},
		{Type: "thinking_end"},
		{Type: "tool_start", ToolID: "t1", ToolName: "Read"},
		{Type: "tool_call", ToolID: "t1", ToolArgs: `{"path":"/tmp/a"}`},
		{Type: "tool_result", ToolID: "t1", Output: "a", IsError: false},
		{Type: "tool_start", ToolID: "t2", ToolName: "Bash"},
		{Type: "tool_call", ToolID: "t2", ToolArgs: `{"command":"ls"}`},
		{Type: "tool_result", ToolID: "t2", Output: "x", IsError: false},
		{Type: "tool_start", ToolID: "t3", ToolName: "Edit"},
		{Type: "tool_call", ToolID: "t3", ToolArgs: `{"path":"/tmp/b"}`},
		{Type: "tool_result", ToolID: "t3", Output: "ok", IsError: false},
		{Type: "thinking", Delta: "After tools, "},
		{Type: "thinking", Delta: "more reasoning."},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := feed(events)
	close(ch)
	tui.consumeEvents(ctx, ch)

	summary := blockSummary(tui)

	// Find indices.
	idx := map[string]int{}
	for i, s := range summary {
		if !strings.HasPrefix(s, "raw:") {
			continue
		}
		switch {
		case strings.Contains(s, "Plan before tools") && idx["think1"] == 0:
			idx["think1"] = i + 1
		case strings.Contains(s, "Read"):
			idx["read"] = i + 1
		case strings.Contains(s, "Bash"):
			idx["bash"] = i + 1
		case strings.Contains(s, "Edit"):
			idx["edit"] = i + 1
		case strings.Contains(s, "After tools, more reasoning"):
			idx["think2"] = i + 1
		}
	}

	for _, k := range []string{"think1", "read", "bash", "edit", "think2"} {
		if idx[k] == 0 {
			t.Fatalf("missing block %q in summary: %v", k, summary)
		}
	}
	if !(idx["think1"] < idx["read"] && idx["read"] < idx["bash"] && idx["bash"] < idx["edit"] && idx["edit"] < idx["think2"]) {
		t.Errorf("blocks out of chronological order: %v", summary)
	}
}

// TestSpinnerStaysOnAfterMidTurnCompact reproduces the field report: an
// auto-compact fires between ReAct iterations of the SAME turn (session.go's
// promptSync triggers it at 98% context usage, then the for loop continues
// into another loop_start — e.g. the model follows up with a MemoSearch call
// per the compaction-checkpoint memory reminder). compact_end turns the
// spinner off (correct — that sub-step finished), but before the loop_start
// fix nothing turned it back on for the continuing work: the agent kept
// calling tools with no spinner, looking frozen/idle when it wasn't.
func TestSpinnerStaysOnAfterMidTurnCompact(t *testing.T) {
	tui := newTestTUIForEvents()
	events := []client.Event{
		{Type: "turn_start"},
		{Type: "loop_start"},
		{Type: "tool_start", ToolID: "t1", ToolName: "Bash"},
		{Type: "tool_call", ToolID: "t1", ToolArgs: `{"command":"ls"}`},
		{Type: "tool_result", ToolID: "t1", Output: "x", IsError: false},
		{Type: "loop_end"},
		{Type: "compact_start"},
		{Type: "compact_end", Summary: "…"},
		// The for loop in promptSync continues into another iteration of the
		// SAME turn — this is the event the fix listens for.
		{Type: "loop_start"},
	}

	// consumeEvents' exit paths (ctx.Done() / channel closed) unconditionally
	// turn the spinner off — that's correct for "stream ended", but it would
	// mask the bug this test targets ("the turn is still going"). So the
	// channel is left open and ctx isn't cancelled until AFTER the assertion:
	// consumeEvents runs in the background and isSpinning() (mutex-protected)
	// is polled from this goroutine once all buffered events are processed.
	ctx, cancel := context.WithCancel(context.Background())
	ch := feed(events)
	done := make(chan struct{})
	go func() {
		tui.consumeEvents(ctx, ch)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for len(ch) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the last buffered event finish processing

	spinning := tui.isSpinning()

	// Only now signal consumeEvents to exit, and wait for it to actually
	// finish before returning — otherwise its goroutine (and the spinner's
	// own internal goroutine, started via Start()) can outlive the test and
	// race the NEXT test's TUI instance on package-global state (e.g.
	// math/rand's default source, used by spinnerLabel()).
	cancel()
	<-done

	if !spinning {
		t.Error("spinner should be back on after loop_start following a mid-turn compact — " +
			"the turn is still working (e.g. the model's post-compaction MemoSearch), " +
			"but nothing re-armed the spinner after compact_end turned it off")
	}
}

// TestSpinnerOffAfterCompactEndThenTurnEnd verifies the OTHER real case:
// compact really is the last thing that happens (e.g. a manual /compact, or
// the model has no more tool calls after compacting) — the spinner must stay
// off once turn_end arrives, not get stuck on forever.
func TestSpinnerOffAfterCompactEndThenTurnEnd(t *testing.T) {
	tui := newTestTUIForEvents()
	events := []client.Event{
		{Type: "turn_start"},
		{Type: "loop_start"},
		{Type: "compact_start"},
		{Type: "compact_end", Summary: "…"},
		{Type: "turn_end"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := feed(events)
	close(ch)
	tui.consumeEvents(ctx, ch)

	if tui.isSpinning() {
		t.Error("spinner should be off after turn_end, even though loop_start re-armed it mid-turn")
	}
}

// TestSpinnerStaysOnAfterMaxIterationsReachedFollowingMidTurnCompact is the
// regression test for the reported bug: the agent reaches its per-turn
// ReAct iteration cap right after an auto-compact fired on the LAST regular
// iteration (session.go's promptSync — ContextUsage >= 0.98 triggers
// compact() between iterations). The real fix lives in session.go: the
// reserved "summarize progress" iteration now emits its OWN loop_start
// right before max_iterations_reached, exactly like every other loop
// iteration — instead of the old asymmetric sequence (a bare loop_end, then
// max_iterations_reached, with no loop_start of its own). That loop_start
// is what re-arms the spinner (same mechanism TestSpinnerStaysOnAfterMidTurnCompact
// covers for the "compact then keep looping" case) — without it, an
// auto-compact on the LAST regular iteration left the spinner off with
// nothing left in the event stream to turn it back on, making the turn
// look frozen right at the "reached the N-iteration limit" warning even
// though requestProgressUpdate's summary was still streaming in right
// behind it. (An earlier version of this fix patched the TUI's spinner
// directly in the max_iterations_reached case instead of fixing this
// missing loop_start/loop_end symmetry in session.go — reverted in favor
// of the structural fix, which also fixes the footer's
// "(turn/max_iterations)" counter below.)
func TestSpinnerStaysOnAfterMaxIterationsReachedFollowingMidTurnCompact(t *testing.T) {
	tui := newTestTUIForEvents()
	events := []client.Event{
		{Type: "turn_start"},
		{Type: "loop_start"},
		{Type: "tool_start", ToolID: "t1", ToolName: "Bash"},
		{Type: "tool_call", ToolID: "t1", ToolArgs: `{"command":"ls"}`},
		{Type: "tool_result", ToolID: "t1", Output: "x", IsError: false},
		{Type: "loop_end"},
		{Type: "compact_start"},
		{Type: "compact_end", Summary: "…"},
		{Type: "loop_end"},
		// session.go now emits the reserved iteration's own loop_start here,
		// right before max_iterations_reached — this is the fix.
		{Type: "loop_start"},
		{Type: "max_iterations_reached", MaxIterations: 50},
	}

	// Same technique as TestSpinnerStaysOnAfterMidTurnCompact: leave the
	// channel open and don't cancel ctx until after asserting, so
	// consumeEvents' unconditional "stream ended" spinner-off on exit
	// doesn't mask the bug.
	ctx, cancel := context.WithCancel(context.Background())
	ch := feed(events)
	done := make(chan struct{})
	go func() {
		tui.consumeEvents(ctx, ch)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for len(ch) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)

	spinning := tui.isSpinning()

	cancel()
	<-done

	if !spinning {
		t.Error("spinner should be back on after the reserved iteration's loop_start (emitted right before " +
			"max_iterations_reached) — requestProgressUpdate's LLM call is still in flight (its text streams " +
			"in right after), and this loop_start should have re-armed it after compact_end turned it off")
	}
}

// TestSummaryTextRendersAfterMaxIterationsReached answers the follow-up
// question this bug raised: does the max-iterations SUMMARY text (streamed
// as ordinary text/text_end events right after max_iterations_reached, per
// session.go's requestProgressUpdate) actually reach the history, or does
// something ALSO swallow it, independent of the spinner? Answer: the "text"
// case in consumeEvents has no dependency on spinner state at all — it only
// checks whether a live Markdown block already exists, so the summary DOES
// get appended and rendered correctly regardless of the spinner bug fixed
// above. This test locks that in as a real regression guard, not just an
// inference from reading the code.
func TestSummaryTextRendersAfterMaxIterationsReached(t *testing.T) {
	tui := newTestTUIForEvents()
	events := []client.Event{
		{Type: "turn_start"},
		{Type: "loop_start"},
		{Type: "tool_start", ToolID: "t1", ToolName: "Bash"},
		{Type: "tool_call", ToolID: "t1", ToolArgs: `{"command":"ls"}`},
		{Type: "tool_result", ToolID: "t1", Output: "x", IsError: false},
		{Type: "loop_end"},
		{Type: "compact_start"},
		{Type: "compact_end", Summary: "…"},
		{Type: "loop_end"},
		// The reserved iteration's own loop_start, emitted right before
		// max_iterations_reached (session.go's fix).
		{Type: "loop_start"},
		{Type: "max_iterations_reached", MaxIterations: 50},
		// requestProgressUpdate's LLM call, streamed exactly like any other
		// turn's response.
		{Type: "text", Delta: "Here's what I got done so far: "},
		{Type: "text", Delta: "created the file and ran the tests."},
		{Type: "text_end"},
		{Type: "loop_end"},
		{Type: "turn_end"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := feed(events)
	close(ch)
	tui.consumeEvents(ctx, ch)

	summary := strings.Join(blockSummary(tui), " | ")
	if !strings.Contains(summary, "created the file and ran the tests") {
		t.Errorf("summary text never reached history — blocks: %s", summary)
	}
}

// infoText renders the footer's info line (path • session (turn/max) [queued])
// as a plain string, for assertions.
func infoText(tui *TUI) string {
	lines := tui.info.Render(500)
	if len(lines) == 0 {
		return ""
	}
	return stripANSI(lines[0])
}

// TestTurnCounterShownWhileWorkingOnly verifies the footer "(turn/max_iterations)"
// indicator: it increments once per loop_start, resets on each new turn_start,
// and is only visible while the agent is actively working — hidden again once
// turn_end arrives, per the user's requested behavior.
func TestTurnCounterShownWhileWorkingOnly(t *testing.T) {
	tui := newTestTUIForEvents()
	tui.sessionName = "kaiban-api-v2"
	tui.maxIterations = 50

	// Before any turn: no counter yet (spinner off).
	tui.updateInfo()
	if strings.Contains(infoText(tui), "(") {
		t.Errorf("counter should not show before any turn starts: %q", infoText(tui))
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := feed([]client.Event{
		{Type: "turn_start"},
		{Type: "loop_start", Loop: 0}, // 1st iteration — agent's own 0-based index
	})
	done := make(chan struct{})
	go func() { tui.consumeEvents(ctx, ch); close(done) }()
	for len(ch) > 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)

	if got := infoText(tui); !strings.Contains(got, "(1/50)") {
		t.Errorf("after 1st loop_start, want \"(1/50)\" in info line, got: %q", got)
	}

	// A second iteration (e.g. after a tool call) increments the counter —
	// read directly from the agent's own Loop index (1), not a client-side
	// increment.
	ch2 := make(chan client.Event, 1)
	ch2 <- client.Event{Type: "loop_start", Loop: 1}
	cancel()
	<-done

	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { tui.consumeEvents(ctx2, ch2); close(done2) }()
	for len(ch2) > 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)

	if got := infoText(tui); !strings.Contains(got, "(2/50)") {
		t.Errorf("after 2nd loop_start, want \"(2/50)\" in info line, got: %q", got)
	}

	cancel2()
	<-done2

	// turn_end hides the counter again.
	ctx3, cancel3 := context.WithTimeout(context.Background(), time.Second)
	defer cancel3()
	ch3 := make(chan client.Event, 1)
	ch3 <- client.Event{Type: "turn_end"}
	close(ch3)
	tui.consumeEvents(ctx3, ch3)

	if got := infoText(tui); strings.Contains(got, "(2/50)") || strings.Contains(got, "/50)") {
		t.Errorf("counter should be hidden after turn_end, got: %q", got)
	}
}

// TestTurnCounterSetFromLoopStartsOwnIndexNotAStaleCarry verifies the
// counter is SET directly from each loop_start event's own Loop index
// (evt.Loop + 1), not incremented from whatever the previous turn/session
// left behind — a stale currTurn (e.g. left over from a prior turn, or from
// resuming a session) must not leak into the new turn's display; the first
// loop_start of a turn always shows exactly its own agent-reported index.
func TestTurnCounterSetFromLoopStartsOwnIndexNotAStaleCarry(t *testing.T) {
	tui := newTestTUIForEvents()
	tui.sessionName = "s"
	tui.maxIterations = 10
	tui.currTurn = 7 // stale value from... anything — must not influence the next assignment

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := feed([]client.Event{
		{Type: "turn_start"},
		{Type: "loop_start", Loop: 0}, // agent's own first iteration of this turn
	})
	close(ch)
	tui.consumeEvents(ctx, ch)

	if got := infoText(tui); !strings.Contains(got, "(1/10)") {
		t.Errorf("loop_start with Loop=0 should show \"(1/10)\" regardless of any stale prior value, got: %q", got)
	}
}

// TestTurnCounterIncrementsForReservedMaxIterationsLoop verifies the OTHER
// half of session.go's structural fix: the reserved "summarize progress"
// iteration's own loop_start (emitted right before max_iterations_reached,
// carrying Loop: maxIterations-1 — see session.go) drives the footer's
// "(turn/max_iterations)" counter exactly like any other iteration — before
// the fix, that counter simply never advanced for this iteration (it only
// ever incremented on loop_start, which this exit path never emitted),
// silently under-reporting how many iterations the turn actually used.
func TestTurnCounterIncrementsForReservedMaxIterationsLoop(t *testing.T) {
	tui := newTestTUIForEvents()
	tui.sessionName = "s"
	tui.maxIterations = 3

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := feed([]client.Event{
		{Type: "turn_start"},
		{Type: "loop_start", Loop: 0}, // iteration 1 (0-based index 0)
		{Type: "loop_end", Loop: 0},
		// The reserved iteration's own loop_start — this is what the fix
		// adds. maxIterations=3, so the reserved index is maxIterations-1=2.
		{Type: "loop_start", Loop: 2}, // iteration 2 (0-based index 2, the reserved summarize-progress one)
		{Type: "max_iterations_reached", MaxIterations: 3},
	})
	close(ch)
	tui.consumeEvents(ctx, ch)

	if got := infoText(tui); !strings.Contains(got, "(3/3)") {
		t.Errorf("reserved iteration's loop_start (Loop=2) should show \"(3/3)\", got: %q", got)
	}
}
