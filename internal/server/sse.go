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
	case types.EventStreamThinkingDelta:
		payload = map[string]any{"type": "thinking", "delta": e.Delta}
	case types.EventStreamTextDelta:
		payload = map[string]any{"type": "text", "delta": e.Delta}
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
	case types.EventFollowUpStart:
		// A queued follow-up prompt is starting; Output carries its text so the
		// frontend can echo it at the right moment (no client-side queue needed).
		payload = map[string]any{"type": "follow_up_start", "text": e.Output}
	case types.EventTurnEnd:
		payload = map[string]any{"type": "turn_end"}
	case types.EventTokens:
		payload = map[string]any{
			"type":           "tokens",
			"input":          e.Tokens.Input,
			"total_output":   e.Tokens.TotalOutput,
			"cache_read":     e.Tokens.CacheRead,
			"cache_write":    e.Tokens.CacheWrite,
			"cost_usd":       e.Tokens.CostUSD,
			"context_usage":  e.Tokens.ContextUsage,
			"context_window": e.Tokens.ContextWindow,
		}
	case types.EventError:
		payload = map[string]any{"type": "error", "message": e.Message}
	case types.EventMaxTurnsReached:
		payload = map[string]any{"type": "max_turns_reached", "max_turns": e.MaxTurns}
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
