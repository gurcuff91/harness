package types

// promptEcho is the wire shape of received_prompt/follow_up_start: text and
// origin, plus message_index (the stored user message — it already exists
// when the event fires) and, when the prompt has images, their metadata
// (never the bytes: GET /api/sessions/{id}/messages/{message_index}/images/{n}).
func promptEcho(typ string, e Event) map[string]any {
	p := map[string]any{"type": typ, "text": e.Output, "origin": e.Origin, "message_index": e.MessageIndex}
	if len(e.Images) > 0 {
		p["images"] = e.Images
	}
	return p
}

// WireEvent is the wire form of an agent event, shared by every frontend that
// sends events as JSON (the HTTP/SSE server, the rpc frontend): one definition.
// The second result is false for event types that are not exposed.
// Field names are snake_case of the Go Event struct fields.
func WireEvent(e Event) (map[string]any, bool) {
	var payload map[string]any

	switch e.Type {
	case EventLoopStart:
		// loop is the 0-based ReAct iteration index (see agent/session.go's
		// promptSync) — the SAME value on both loop_start and loop_end for a
		// given iteration (they're a matched open/close pair, not two
		// separate counters). Always included, never omitted, even for
		// iteration 0 — omitempty on a plain int would drop it exactly when
		// it's 0, indistinguishable on the wire from "field absent".
		payload = map[string]any{"type": "loop_start", "loop": e.Loop}
	case EventLoopEnd:
		payload = map[string]any{"type": "loop_end", "loop": e.Loop}
	case EventStreamThinkingDelta:
		payload = map[string]any{"type": "thinking", "delta": e.Delta}
	case EventStreamThinkingEnd:
		payload = map[string]any{"type": "thinking_end"}
	case EventStreamTextDelta:
		payload = map[string]any{"type": "text", "delta": e.Delta}
	case EventStreamTextEnd:
		payload = map[string]any{"type": "text_end"}
	case EventToolStart:
		payload = map[string]any{"type": "tool_start", "tool_name": e.ToolName, "tool_id": e.ToolID}
	case EventToolArgsDelta:
		payload = map[string]any{"type": "tool_args", "tool_name": e.ToolName, "tool_id": e.ToolID, "delta": e.Delta}
	case EventToolCall:
		payload = map[string]any{"type": "tool_call", "tool_name": e.ToolName, "tool_id": e.ToolID, "tool_args": e.ToolArgs}
	case EventToolResult:
		payload = map[string]any{
			"type":      "tool_result",
			"tool_name": e.ToolName,
			"tool_id":   e.ToolID,
			"output":    e.Output,
			// Fractional milliseconds (from microseconds) so sub-ms tools — e.g. an
			// in-memory MemoSearch — don't truncate to 0 and drop the [time] tag.
			"duration": float64(e.Duration.Microseconds()) / 1000.0,
			"is_error": e.IsError,
		}
	case EventTurnStart:
		payload = map[string]any{"type": "turn_start"}
	case EventReceivedPrompt:
		// An immediate (non-queued) prompt was received; text + origin let the
		// frontend echo it (even for prompts it didn't originate, e.g. scheduled).
		payload = promptEcho("received_prompt", e)
	case EventFollowUpStart:
		// A queued follow-up prompt is starting; text + origin so the frontend can
		// echo it at the right moment (no client-side queue needed).
		payload = promptEcho("follow_up_start", e)
	case EventTurnEnd:
		payload = map[string]any{"type": "turn_end"}
	case EventSubagentStart:
		payload = map[string]any{"type": "subagent_start", "name": e.Subagent}
	case EventSubagentStop:
		payload = map[string]any{"type": "subagent_stop", "name": e.Subagent, "result": e.SubagentResult}
	case EventRetry:
		// A transient network failure (dead/stalled connection, 502/503/504)
		// is being retried on a new connection — non-fatal, for display.
		payload = map[string]any{"type": "retry", "attempt": e.Attempt, "max": e.MaxAttempts, "error": e.Message}
	case EventTokens:
		// Two semantics, both intentional (see types.TokenUsage):
		//   live context  → input, context_usage, context_window
		//   session total → total_input, total_output, cache_read,
		//                   cache_write, cost_usd
		// A plain map (not a struct with omitempty) so a zero is always sent
		// explicitly: 0 cache reads or 0.0 usage right after a compaction are
		// MEANINGFUL, and dropping them would be indistinguishable on the wire
		// from "the field wasn't reported".
		payload = map[string]any{
			"type":           "tokens",
			"input":          e.Tokens.Input,
			"context_usage":  e.Tokens.ContextUsage,
			"context_window": e.Tokens.ContextWindow,
			"total_input":    e.Tokens.TotalInput,
			"total_output":   e.Tokens.TotalOutput,
			"cache_read":     e.Tokens.CacheRead,
			"cache_write":    e.Tokens.CacheWrite,
			"cost_usd":       e.Tokens.CostUSD,
		}
	case EventError:
		p := map[string]any{"type": "error", "message": e.Message}
		if len(e.Details) > 0 {
			p["details"] = e.Details
		}
		payload = p
	case EventMaxIterationsReached:
		payload = map[string]any{"type": "max_iterations_reached", "max_iterations": e.MaxIterations}
	case EventCompactStart:
		payload = map[string]any{"type": "compact_start"}
	case EventCompactEnd:
		payload = map[string]any{"type": "compact_end", "summary": e.Summary}
	case EventStop:
		payload = map[string]any{"type": "stop"}
	default:
		return nil, false // not exposed
	}
	return payload, true
}
