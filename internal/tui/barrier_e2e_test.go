package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// Hostile text reaching the screen through each route a TUI really has. The events are fed through
// the real consumeEvents, the lines come from the real component tree, and they go through the same
// barrier doRender applies — so this is the path a byte takes to the terminal, minus the PTY.
const (
	hOSC52 = "\x1b]52;c;UFdOLUNMSVA=\x07"
	hOSC0  = "\x1b]0;PWN\x07"
	hCSI6n = "\x1b[6n"
	hDCS   = "\x1b" + "Pq#PWN\x1b\\"
	hC1    = "\u009b6n"
)

var hostileAll = hOSC52 + hOSC0 + hCSI6n + hDCS + hC1 + "\x1b[?1049h"

// mount puts the real components in the render tree. It must happen BEFORE anything is drawn: the
// renderer's own scheduled frames read the tree from another goroutine.
//
// It also stops the renderer's own timer-driven frames. They are irrelevant here — the tests render
// by hand, at the width they choose — and they would read the history from another goroutine while
// consumeEvents appends to it (production serializes that with the TUI's own lock; a test that
// renders without it would race on its own).
func mount(tu *TUI) *TUI {
	tu.tui.AddChild(tu.history)
	tu.tui.AddChild(tu.spinner)
	tu.tui.AddChild(tu.info)
	tu.tui.AddChild(tu.footer)
	tu.tui.Stop() // no scheduled frames; Render() still works
	return tu
}

// screenLines is everything the TUI would draw, after the barrier.
func screenLines(tu *TUI, width int) []string {
	tu.updateInfo()
	raw := tu.tui.Render(width)
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = ansi.SafeLine(l)
	}
	return out
}

func assertHarmless(t *testing.T, route string, lines []string) {
	t.Helper()
	all := strings.Join(lines, "\n")
	for name, needle := range map[string]string{
		"OSC 52": "\x1b]52", "OSC 0": "\x1b]0;", "CSI 6n": "\x1b[6n", "DCS": "\x1b" + "P", "alt screen": "\x1b[?1049", "C1": "\u009b",
	} {
		if strings.Contains(all, needle) {
			t.Errorf("%s: %s reached the screen as a live sequence", route, name)
		}
	}
	if strings.Contains(all, "\x07") {
		t.Errorf("%s: a live BEL reached the screen", route)
	}
}

func play3(t *testing.T, evs ...client.Event) *TUI {
	t.Helper()
	tu := mount(newTestTUIForEvents())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ch := feed(evs)
	close(ch)
	tu.consumeEvents(ctx, ch)
	return tu
}

func TestEveryRouteToTheScreenIsHarmless(t *testing.T) {
	for route, evs := range map[string][]client.Event{
		"assistant text":   {{Type: "turn_start"}, {Type: "text", Delta: "model says: " + hostileAll + " end"}, {Type: "turn_end"}},
		"thinking":         {{Type: "turn_start"}, {Type: "thinking", Delta: "thinking: " + hostileAll}, {Type: "thinking_end"}, {Type: "turn_end"}},
		"tool call header": {{Type: "tool_start", ToolID: "a", ToolName: "Bash"}, {Type: "tool_call", ToolID: "a", ToolName: "Bash", ToolArgs: `{"command":"echo ` + strings.NewReplacer("\x1b", `\u001b`, "\x07", `\u0007`).Replace(hostileAll) + `"}`}},
		"tool result (1 line)": {{Type: "tool_start", ToolID: "b", ToolName: "Bash"}, {Type: "tool_call", ToolID: "b", ToolName: "Bash", ToolArgs: `{"command":"x"}`},
			{Type: "tool_result", ToolID: "b", Output: "RESULT " + hostileAll}},
		"tool result (error)": {{Type: "tool_start", ToolID: "c", ToolName: "Bash"}, {Type: "tool_call", ToolID: "c", ToolName: "Bash", ToolArgs: `{"command":"x"}`},
			{Type: "tool_result", ToolID: "c", Output: "BOOM " + hostileAll, IsError: true}},
		"error event":  {{Type: "error", Message: "provider said " + hostileAll, Details: map[string]any{"body": hostileAll}}},
		"retry notice": {{Type: "retry", Attempt: 1, Max: 3, Error: hostileAll}},
		"prompt echo":  {{Type: "received_prompt", Text: "prompt " + hostileAll, Origin: "user"}},
		"queued echo":  {{Type: "follow_up_start", Text: "queued " + hostileAll, Origin: "user"}},
	} {
		t.Run(route, func(t *testing.T) {
			tu := play3(t, evs...)
			lines := screenLines(tu, 120)
			assertHarmless(t, route, lines)
			if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "␛") && !strings.Contains(ansi.StripEscapes(joined), "RESULT") && route != "tool call header" {
				t.Logf("%s: output %q", route, joined)
			}
		})
	}
}

func TestTheSessionNameInTheFooterIsHarmless(t *testing.T) {
	tu := mount(newTestTUIForEvents())
	tu.setSessionName("name " + hostileAll)
	tu.model = "fake/m1"
	assertHarmless(t, "session name in the footer", screenLines(tu, 120))
}

// The tool-result summary is cleaned BEFORE it is drawn, by stripANSI: it used to know only the few
// sequences the TUI itself emits, and let CSI 6n and DCS through.
func TestAToolResultSummaryCarriesNoEscapeAtAll(t *testing.T) {
	tu := newTestTUIForEvents()
	got := tu.formatToolResult("ok "+hostileAll+" done", 0, false)
	for _, bad := range []string{"\x1b]52", "\x1b]0;", "\x1b[6n", "\x1b" + "P", "\x1b[?1049", "\u009b", "\x07"} {
		if strings.Contains(got, bad) {
			t.Errorf("the summary still carries %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "ok") || !strings.Contains(got, "done") {
		t.Errorf("the text around the sequences was lost: %q", got)
	}
}

func TestStripANSIKnowsTheWholeShapeOfASequence(t *testing.T) {
	for in, want := range map[string]string{
		"a\x1b[6nb":                 "ab",
		"a\x1b[?1049hb":             "ab",
		"a\x1b[5 qb":                "ab", // an intermediate byte
		"a\x1b[>1ub":                "ab",
		"a\x1b" + "Pq#X\x1b\\b":     "ab",
		"a\x1b]52;c;QQ==\x07b":      "ab",
		"a\x1b]0;t\x1b\\b":          "ab",
		"a\x1b_apc\x1b\\b":          "ab",
		"a\x1b^pm\x1b\\b":           "ab",
		"a\x1bcb":                   "ab", // RIS, two bytes
		"a\x1b7b":                   "ab",
		"a\x1b(0b":                  "ab", // charset designation
		"a\x1b[1mbold\x1b[0mb":      "aboldb",
		"keep\nnewline\tand tab":    "keep\nnewline\tand tab",
		"a\x00b\x07c\x7fd\re":       "abcde",
		"x\u009b6ny":                "x6ny",
		"unterminated \x1b]0;title": "unterminated 0;title", // the text after ESC ] is not swallowed
		"tail ESC \x1b":             "tail ESC ",
		"a\x1b[1\x01b":              "a1b", // a CSI with no valid final byte: the introducer goes, the text stays
		"":                          "",
	} {
		if got := ansi.StripEscapes(in); got != want {
			t.Errorf("StripEscapes(%q) = %q, want %q", in, got, want)
		}
	}
}

// The barrier must not touch what the TUI draws by itself. Everything below is realistic output —
// markdown with every construct (headings, emphasis, a link and an autolink, lists, a task list, a
// quote, a code block, a table, a rule, emoji and CJK), thinking, a tool call with its result and
// an error result, notices, the footer — rendered by the real components at three widths, and every
// line must come back from the barrier byte for byte. It also proves the sample is not trivial:
// it has to contain styled text and an OSC 8 hyperlink, or it would prove nothing.
const ownMarkdown = "# Heading one\n\n## Heading two\n\nSome **bold**, *italic*, ~~strike~~, `code` and a [link](https://example.com/x?a=1&b=2) plus <https://auto.example>.\n\n" +
	"- bullet one\n- [ ] task\n- [x] done\n  - nested\n\n1. first\n2. second\n\n> a quote\n\n```go\nfunc main() { fmt.Println(\"hi\") }\n```\n\n" +
	"| col a | col b |\n|---|---|\n| 1 | 2 |\n| 33 | 44 |\n\n---\n\nEnd with emoji 🚀 and 日本語.\n"

func TestTheBarrierLeavesTheTUIsOwnOutputByteForByte(t *testing.T) {
	tu := mount(newTestTUIForEvents())
	tu.setSessionName("Fixed name")
	tu.model = "fake/m1"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ch := feed([]client.Event{
		{Type: "turn_start"},
		{Type: "thinking", Delta: "thinking about it"}, {Type: "thinking_end"},
		{Type: "tool_start", ToolID: "t1", ToolName: "Bash"},
		{Type: "tool_call", ToolID: "t1", ToolName: "Bash", ToolArgs: `{"command":"echo hello"}`},
		{Type: "tool_result", ToolID: "t1", Output: "hello"},
		{Type: "tool_start", ToolID: "t2", ToolName: "Read"},
		{Type: "tool_call", ToolID: "t2", ToolName: "Read", ToolArgs: `{"path":"/tmp/x"}`},
		{Type: "tool_result", ToolID: "t2", Output: "a\nb\nc", IsError: true},
		{Type: "text", Delta: ownMarkdown},
		{Type: "retry", Attempt: 1, Max: 3, Error: "boom"},
		{Type: "error", Message: "something failed", Details: map[string]any{"k": "v"}},
		{Type: "stop"},
		{Type: "turn_end"},
	})
	close(ch)
	tu.consumeEvents(ctx, ch)
	tu.updateInfo()

	lines, styled, links := 0, 0, 0
	for _, w := range []int{40, 80, 140} {
		for _, l := range tu.tui.Render(w) {
			lines++
			if strings.Contains(l, "\x1b[") {
				styled++
			}
			if strings.Contains(l, "\x1b]8;;") {
				links++
			}
			if got := ansi.SafeLine(l); got != l {
				t.Errorf("width %d: the barrier changed a line the TUI drew itself:\n  in : %q\n  out: %q", w, l, got)
			}
		}
	}
	if lines < 60 || styled < 30 || links < 3 {
		t.Fatalf("the sample is too thin to prove anything: %d lines, %d styled, %d with a hyperlink", lines, styled, links)
	}
}
