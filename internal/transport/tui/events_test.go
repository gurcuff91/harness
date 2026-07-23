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
