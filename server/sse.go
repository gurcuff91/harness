package server

import (
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// formatEvent converts an agent event to a JSON SSE data line.
// Returns nil for event types not exposed via SSE.
// SSE field names are snake_case of the Go Event struct fields.
func formatEvent(e types.Event) []byte {
	var payload any

	switch e.Type {
	case types.EventLoopStart:
		// loop is the 0-based ReAct iteration index (see agent/session.go's
		// promptSync) — the SAME value on both loop_start and loop_end for a
		// given iteration (they're a matched open/close pair, not two
		// separate counters). Always included, never omitted, even for
		// iteration 0 — omitempty on a plain int would drop it exactly when
		// it's 0, indistinguishable on the wire from "field absent".
		payload = map[string]any{"type": "loop_start", "loop": e.Loop}
	case types.EventLoopEnd:
		payload = map[string]any{"type": "loop_end", "loop": e.Loop}
	case types.EventStreamThinkingDelta:
		payload = map[string]any{"type": "thinking", "delta": e.Delta}
	case types.EventStreamThinkingEnd:
		payload = map[string]any{"type": "thinking_end"}
	case types.EventStreamTextDelta:
		payload = map[string]any{"type": "text", "delta": e.Delta}
	case types.EventStreamTextEnd:
		payload = map[string]any{"type": "text_end"}
	case types.EventToolStart:
		payload = map[string]any{"type": "tool_start", "tool_name": e.ToolName, "tool_id": e.ToolID}
	case types.EventToolArgsDelta:
		payload = map[string]any{"type": "tool_args", "tool_name": e.ToolName, "tool_id": e.ToolID, "delta": e.Delta}
	case types.EventToolCall:
		payload = map[string]any{"type": "tool_call", "tool_name": e.ToolName, "tool_id": e.ToolID, "tool_args": e.ToolArgs}
	case types.EventToolResult:
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
	case types.EventTurnStart:
		payload = map[string]any{"type": "turn_start"}
	case types.EventReceivedPrompt:
		// An immediate (non-queued) prompt was received; text + origin let the
		// frontend echo it (even for prompts it didn't originate, e.g. scheduled).
		payload = map[string]any{"type": "received_prompt", "text": e.Output, "origin": e.Origin}
	case types.EventFollowUpStart:
		// A queued follow-up prompt is starting; text + origin so the frontend can
		// echo it at the right moment (no client-side queue needed).
		payload = map[string]any{"type": "follow_up_start", "text": e.Output, "origin": e.Origin}
	case types.EventTurnEnd:
		payload = map[string]any{"type": "turn_end"}
	case types.EventTokens:
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
	case types.EventError:
		p := map[string]any{"type": "error", "message": e.Message}
		if len(e.Details) > 0 {
			p["details"] = e.Details
		}
		payload = p
	case types.EventMaxIterationsReached:
		payload = map[string]any{"type": "max_iterations_reached", "max_iterations": e.MaxIterations}
	case types.EventCompactStart:
		payload = map[string]any{"type": "compact_start"}
	case types.EventCompactEnd:
		payload = map[string]any{"type": "compact_end", "summary": e.Summary}
	case types.EventStop:
		payload = map[string]any{"type": "stop"}
	default:
		return nil // not exposed
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return []byte(fmt.Sprintf("data: %s\n\n", string(b)))
}
