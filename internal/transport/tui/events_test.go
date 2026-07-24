package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/internal/transport/tui/components"
	"github.com/gurcuff91/harness/internal/transport/tui/render"
)

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
	events := []map[string]any{
		{"type": "turn_start"},
		{"type": "thinking", "delta": "First reasoning "},
		{"type": "thinking", "delta": "block."},
		{"type": "thinking_end"},
		{"type": "tool_start", "tool_id": "t1", "tool_name": "Bash"},
		{"type": "tool_args", "tool_id": "t1", "delta": `{"command":"ls"}`},
		{"type": "tool_call", "tool_id": "t1", "tool_args": `{"command":"ls"}`},
		{"type": "tool_result", "tool_id": "t1", "output": "file.txt", "is_error": false},
		{"type": "thinking", "delta": "Second reasoning "},
		{"type": "thinking", "delta": "after tool."},
		{"type": "thinking_end"},
	}

	// Drain via consumeEvents on its own goroutine and close the channel when done.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := make(chan map[string]any, len(events))
	for _, e := range events {
		ch <- e
	}
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
	events := []map[string]any{
		{"type": "turn_start"},
		{"type": "thinking", "delta": "Plan "},
		{"type": "thinking", "delta": "before tools."},
		{"type": "thinking_end"},
		{"type": "tool_start", "tool_id": "t1", "tool_name": "Read"},
		{"type": "tool_call", "tool_id": "t1", "tool_args": `{"path":"/tmp/a"}`},
		{"type": "tool_result", "tool_id": "t1", "output": "a", "is_error": false},
		{"type": "tool_start", "tool_id": "t2", "tool_name": "Bash"},
		{"type": "tool_call", "tool_id": "t2", "tool_args": `{"command":"ls"}`},
		{"type": "tool_result", "tool_id": "t2", "output": "x", "is_error": false},
		{"type": "tool_start", "tool_id": "t3", "tool_name": "Edit"},
		{"type": "tool_call", "tool_id": "t3", "tool_args": `{"path":"/tmp/b"}`},
		{"type": "tool_result", "tool_id": "t3", "output": "ok", "is_error": false},
		{"type": "thinking", "delta": "After tools, "},
		{"type": "thinking", "delta": "more reasoning."},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := make(chan map[string]any, len(events))
	for _, e := range events {
		ch <- e
	}
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
	events := []map[string]any{
		{"type": "turn_start"},
		{"type": "loop_start"},
		{"type": "tool_start", "tool_id": "t1", "tool_name": "Bash"},
		{"type": "tool_call", "tool_id": "t1", "tool_args": `{"command":"ls"}`},
		{"type": "tool_result", "tool_id": "t1", "output": "x", "is_error": false},
		{"type": "loop_end"},
		{"type": "compact_start"},
		{"type": "compact_end", "summary": "…"},
		// The for loop in promptSync continues into another iteration of the
		// SAME turn — this is the event the fix listens for.
		{"type": "loop_start"},
	}

	// consumeEvents' exit paths (ctx.Done() / channel closed) unconditionally
	// turn the spinner off — that's correct for "stream ended", but it would
	// mask the bug this test targets ("the turn is still going"). So the
	// channel is left open and ctx isn't cancelled until AFTER the assertion:
	// consumeEvents runs in the background and isSpinning() (mutex-protected)
	// is polled from this goroutine once all buffered events are processed.
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan map[string]any, len(events))
	for _, e := range events {
		ch <- e
	}
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
	events := []map[string]any{
		{"type": "turn_start"},
		{"type": "loop_start"},
		{"type": "compact_start"},
		{"type": "compact_end", "summary": "…"},
		{"type": "turn_end"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := make(chan map[string]any, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	tui.consumeEvents(ctx, ch)

	if tui.isSpinning() {
		t.Error("spinner should be off after turn_end, even though loop_start re-armed it mid-turn")
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

// TestTurnCounterShownWhileWorkingOnly verifies the footer "(turn/max_turns)"
// indicator: it increments once per loop_start, resets on each new turn_start,
// and is only visible while the agent is actively working — hidden again once
// turn_end arrives, per the user's requested behavior.
func TestTurnCounterShownWhileWorkingOnly(t *testing.T) {
	tui := newTestTUIForEvents()
	tui.sessionName = "kaiban-api-v2"
	tui.maxTurns = 50

	// Before any turn: no counter yet (spinner off).
	tui.updateInfo()
	if strings.Contains(infoText(tui), "(") {
		t.Errorf("counter should not show before any turn starts: %q", infoText(tui))
	}

	events := []map[string]any{
		{"type": "turn_start"},
		{"type": "loop_start"}, // 1st iteration
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan map[string]any, len(events))
	for _, e := range events {
		ch <- e
	}
	done := make(chan struct{})
	go func() { tui.consumeEvents(ctx, ch); close(done) }()
	for len(ch) > 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)

	if got := infoText(tui); !strings.Contains(got, "(1/50)") {
		t.Errorf("after 1st loop_start, want \"(1/50)\" in info line, got: %q", got)
	}

	// A second iteration (e.g. after a tool call) increments the counter.
	ch2 := make(chan map[string]any, 1)
	ch2 <- map[string]any{"type": "loop_start"}
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
	ch3 := make(chan map[string]any, 1)
	ch3 <- map[string]any{"type": "turn_end"}
	close(ch3)
	tui.consumeEvents(ctx3, ch3)

	if got := infoText(tui); strings.Contains(got, "(2/50)") || strings.Contains(got, "/50)") {
		t.Errorf("counter should be hidden after turn_end, got: %q", got)
	}
}

// TestTurnCounterResetsOnNewTurn verifies a fresh turn_start resets the
// counter to 0 (so the first loop_start of the new turn shows "(1/max)", not
// a continuation of the previous turn's count).
func TestTurnCounterResetsOnNewTurn(t *testing.T) {
	tui := newTestTUIForEvents()
	tui.sessionName = "s"
	tui.maxTurns = 10
	tui.currTurn = 7 // simulate a previous turn that reached iteration 7

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := make(chan map[string]any, 2)
	ch <- map[string]any{"type": "turn_start"}
	ch <- map[string]any{"type": "loop_start"}
	close(ch)
	tui.consumeEvents(ctx, ch)

	if got := infoText(tui); !strings.Contains(got, "(1/10)") {
		t.Errorf("new turn's first loop_start should show \"(1/10)\", got: %q", got)
	}
}
