package tuiv3

import (
	"context"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
	"github.com/gurcuff91/harness/transport/tui-v3/components"
)

// streamEvents consumes the SSE event stream and renders each event into the
// source-backed block history. Assistant text accumulates into a live Markdown
// block (re-rendered on resize); thinking and tool calls use RawBlocks.
func (t *TUI) streamEvents(ctx context.Context) {
	events, err := t.client.StreamEvents(ctx, t.sessionID)
	if err != nil {
		t.addRaw(ansi.Err("✘ " + err.Error()))
		t.setSpinning(false)
		return
	}

	toolNames := make(map[string]string)
	argBufs := make(map[string]string)
	var thinkBlk *components.RawBlock // live thinking block
	var thinkBuf string

	for {
		select {
		case <-ctx.Done():
			t.setSpinning(false)
			return
		case evt, ok := <-events:
			if !ok {
				t.setSpinning(false)
				return
			}
			typ, _ := evt["type"].(string)
			switch typ {

			case "turn_start":
				t.lastTurnText.Reset()
				t.mu.Lock()
				t.liveMD = nil
				t.mu.Unlock()
				thinkBlk, thinkBuf = nil, ""
				t.setSpinning(true)

			case "thinking":
				delta, _ := evt["delta"].(string)
				thinkBuf += delta
				if thinkBlk == nil {
					thinkBlk = t.addSection("thinking", ansi.Dim+ansi.Ital+thinkBuf+ansi.Reset)
				} else {
					thinkBlk.SetText(ansi.Dim + ansi.Ital + thinkBuf + ansi.Reset)
					t.tui.RequestRender(false)
				}

			case "text":
				delta, _ := evt["delta"].(string)
				t.lastTurnText.WriteString(delta)
				thinkBlk = nil // thinking section is closed once real text starts
				t.mu.Lock()
				if t.liveMD == nil {
					t.beginSection("text")
					t.liveMD = components.NewMarkdown("")
					t.history.Add(t.liveMD)
				}
				t.liveMD.Append(delta)
				t.mu.Unlock()
				t.tui.RequestRender(false)

			case "tool_start":
				name, _ := evt["tool_name"].(string)
				toolID, _ := evt["tool_id"].(string)
				toolNames[toolID] = name
				argBufs[toolID] = ""
				thinkBlk = nil
				// Two blocks per tool: the header (icon + name + args) and the
				// result line. beginSection closes the live markdown block so any
				// post-tool response starts a NEW block below — chronological order.
				t.mu.Lock()
				// A spacer between consecutive tools (beginSection only spaces on a
				// kind change, so back-to-back tools need their own breathing room).
				if t.lastKind == "tool" && t.history.Len() > 0 {
					t.history.Add(components.NewSpacer(1))
				}
				t.beginSection("tool")
				argBlk := components.NewRawBlock(t.toolHeader(name, ""))
				resBlk := components.NewRawBlock("")
				t.history.Add(argBlk)
				t.history.Add(resBlk)
				t.toolArgs[toolID] = argBlk
				t.toolBlk[toolID] = resBlk
				t.mu.Unlock()
				t.tui.RequestRender(false)

			case "tool_args":
				delta, _ := evt["delta"].(string)
				if delta == "" {
					break
				}
				toolID, _ := evt["tool_id"].(string)
				argBufs[toolID] += delta
				if b := t.toolArgs[toolID]; b != nil {
					b.SetText(t.toolHeader(toolNames[toolID], argBufs[toolID]))
					t.tui.RequestRender(false)
				}

			case "tool_call":
				toolID, _ := evt["tool_id"].(string)
				name := toolNames[toolID]
				if name == "" {
					name, _ = evt["tool_name"].(string)
				}
				toolArgs, _ := evt["tool_args"].(string)
				args := strings.TrimSpace(toolArgs)
				args = strings.TrimPrefix(args, "{")
				args = strings.TrimSuffix(args, "}")
				args = strings.TrimSpace(args)
				if b := t.toolArgs[toolID]; b != nil {
					b.SetText(t.toolHeader(name, args))
				}
				colorFn, _ := toolStyle(name)
				if b := t.toolBlk[toolID]; b != nil {
					b.SetText(colorFn("⧖") + ansi.Dimmed(" Executing..."))
				}
				t.tui.RequestRender(false)

			case "tool_result":
				toolID, _ := evt["tool_id"].(string)
				output, _ := evt["output"].(string)
				dur, _ := floatFromMap(evt, "duration")
				isErr, _ := evt["is_error"].(bool)
				result := t.formatToolResult(output, dur, isErr)
				if b := t.toolBlk[toolID]; b != nil {
					b.SetText(result)
				}
				t.mu.Lock()
				delete(t.toolBlk, toolID)
				delete(t.toolArgs, toolID)
				t.mu.Unlock()
				t.tui.RequestRender(false)

			case "compact_start":
				t.compactStart = nowMonotonic()
				t.addRaw(ansi.Accent(ansi.Bold + "◎ Compacting"))

			case "compact_end":
				t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("done"))
				t.setSpinning(false)
				t.updateInfo()

			case "stop":
				t.addRaw(ansi.Warn("⏹ Stopped"))
				t.setSpinning(false)

			case "tokens":
				t.stats.input, _ = intFromMap(evt, "input")
				t.stats.output, _ = intFromMap(evt, "total_output")
				t.stats.cacheRead, _ = intFromMap(evt, "cache_read")
				t.stats.cacheWrite, _ = intFromMap(evt, "cache_write")
				t.stats.cost, _ = floatFromMap(evt, "cost_usd")
				t.stats.contextPct, _ = floatFromMap(evt, "context_usage")
				t.stats.contextWin, _ = intFromMap(evt, "context_window")
				t.updateInfo()

			case "turn_end":
				// The live markdown block already holds the full assistant text;
				// it stays in history as a source-backed block (re-renders on
				// resize). Just detach the live pointer.
				t.mu.Lock()
				t.liveMD = nil
				t.mu.Unlock()
				thinkBlk, thinkBuf = nil, ""
				// Dispatch any locally queued prompt.
				if t.queueCount > 0 && len(t.localQueue) > 0 {
					msg := t.localQueue[0]
					t.localQueue = t.localQueue[1:]
					t.queueCount--
					t.addRaw(ansi.Primary("❯ " + msg))
					t.setSpinning(true)
					t.updateInfo()
				} else {
					t.setSpinning(false)
				}

			case "error":
				msg, _ := evt["message"].(string)
				t.addRaw(ansi.Err("✘ " + msg))
				t.setSpinning(false)
			}
		}
	}
}

// toolHeader formats a tool-call header line: "icon Name(args)" with dim args.
func (t *TUI) toolHeader(name, args string) string {
	colorFn, icon := toolStyle(name)
	h := colorFn(ansi.Bold+icon+" "+name) + colorFn("(")
	if args != "" {
		h += ansi.Dimmed(args)
	}
	return h + colorFn(")")
}

// formatToolResult renders the one-line result summary (✔/✘ + duration).
func (t *TUI) formatToolResult(output string, dur float64, isErr bool) string {
	if isErr {
		clean := stripANSI(strings.TrimSpace(output))
		lines := strings.Split(clean, "\n")
		first := strings.TrimSpace(lines[0])
		var sb strings.Builder
		sb.WriteString(ansi.Err("✘") + " " + ansi.Dimmed(fmt.Sprintf("[%s] %s", formatDur(dur), first)))
		shown := 0
		for _, l := range lines[1:] {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if shown >= 2 {
				break
			}
			sb.WriteString("\n  " + ansi.Dimmed(l))
			shown++
		}
		return sb.String()
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	count := len(lines)
	if count == 1 && lines[0] == "" {
		count = 0
	}
	return ansi.Accent("✔") + " " + ansi.Dimmed(fmt.Sprintf("[%s] (%d lines)", formatDur(dur), count))
}

// setSpinning toggles the spinner animation.
func (t *TUI) setSpinning(on bool) {
	t.spinning = on
	if on {
		t.spinner.SetMessage(spinnerLabel())
		t.spinner.Start()
	} else {
		t.spinner.Stop()
	}
	t.tui.RequestRender(false)
}
