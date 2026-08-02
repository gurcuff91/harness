package acp

import (
	"fmt"

	"github.com/gurcuff91/harness/client"
)

// toolKindByName maps a Harness tool name to its ACP ToolKind category,
// purely by name — a small fixed table, not a per-tool annotation, since
// this transport must not touch agent/tools/. Unlisted tools (Skill,
// Subagent, MemoWrite/Search/Delete, Schedule*, Colleague*) fall back to
// toolKindOther.
var toolKindByName = map[string]string{
	"Bash":  toolKindExec,
	"Read":  toolKindRead,
	"Write": toolKindEdit,
	"Edit":  toolKindEdit,
	"Fetch": toolKindFetch,
}

func toolKind(name string) string {
	if k, ok := toolKindByName[name]; ok {
		return k
	}
	return toolKindOther
}

// promptOutcome is what a prompt turn resolved to — carried back through the
// pump's return so the caller can reply to the pending session/prompt
// request with the right stopReason, or surface an error.
type promptOutcome struct {
	stopReason string
	err        *rpcError // set only when the turn ended in EventError
}

// pumpEvents reads Harness's SSE event stream for one turn and translates
// each event into 0 or 1 "session/update" notification, written immediately
// to the ACP client. It returns once the turn is over — on "turn_end" (the
// normal case), "stop" (cancelled), "max_iterations_reached", or "error" —
// with the outcome the caller uses to resolve the pending session/prompt.
//
// stopOnCompactEnd changes what "turn over" means for the /compact command
// specifically: unlike a normal prompt or "/skill:*" (both real ReAct turns
// that end in EventTurnEnd), Session.Compact() runs standalone and only ever
// emits EventCompactStart → EventCompactEnd (or EventError on failure) — it
// never emits a turn_end at all, so pumpEvents would otherwise block forever
// waiting for one. Pass true only from the /compact command path; every
// other caller (normal prompts, "/skill:*") leaves it false.
//
// pendingEdits tracks in-flight Edit/Write tool calls awaiting their "after"
// snapshot for diff building (see diff.go) — keyed by tool call ID so
// concurrent tool calls within one turn (the ReAct loop runs them in
// parallel) don't clobber each other.
func pumpEvents(c *conn, sessionID string, events <-chan client.Event, stopOnCompactEnd bool) promptOutcome {
	pendingEdits := map[string]pendingEdit{}

	for evt := range events {
		switch evt.Type {
		case "text":
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "agent_message_chunk",
				Content:       ptr(textBlock(evt.Delta)),
			})

		case "thinking":
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "agent_thought_chunk",
				Content:       ptr(textBlock(evt.Delta)),
			})

		case "tool_start":
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "tool_call",
				ToolCallID:    evt.ToolID,
				Title:         evt.ToolName,
				Kind:          toolKind(evt.ToolName),
				Status:        toolStatusPending,
			})

		case "tool_call":
			if evt.ToolName == "Edit" || evt.ToolName == "Write" {
				if pre, ok := capturePreEditState(evt.ToolArgs); ok {
					pendingEdits[evt.ToolID] = pre
				}
			}
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "tool_call_update",
				ToolCallID:    evt.ToolID,
				Status:        toolStatusInProgress,
				RawInput:      []byte(evt.ToolArgs),
			})

		case "tool_result":
			status := toolStatusCompleted
			if evt.IsError {
				status = toolStatusFailed
			}
			content := contentOnly(evt.Output)
			if !evt.IsError {
				if pre, ok := pendingEdits[evt.ToolID]; ok {
					if diff, ok := buildDiffContent(pre); ok {
						content = []toolCallContent{diff}
					}
				}
			}
			delete(pendingEdits, evt.ToolID)
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "tool_call_update",
				ToolCallID:    evt.ToolID,
				Status:        status,
				ToolContent:   content,
			})

		case "tokens":
			var cost *usageCost
			if evt.CostUSD > 0 {
				cost = &usageCost{Amount: evt.CostUSD, Currency: "USD"}
			}
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "usage_update",
				Used:          int64(evt.Input),
				Size:          int64(evt.ContextWindow),
				Cost:          cost,
			})

		case "compact_start":
			// Compaction can fire either from an explicit /compact command or
			// AUTOMATICALLY mid-turn (agent/session.go compacts on its own
			// between ReAct iterations once context usage crosses 98%) — the
			// user never asked for it in that second case, so a visible chunk
			// is the only way Zed's UI ever reflects that the context was just
			// rewritten out from under the conversation it's displaying.
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "agent_message_chunk",
				Content:       ptr(textBlock("⏳ Compacting context...")),
			})

		case "compact_end":
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "agent_message_chunk",
				Content:       ptr(textBlock("✓ Context compacted.")),
			})
			if stopOnCompactEnd {
				return promptOutcome{stopReason: stopReasonEndTurn}
			}

		case "turn_end":
			return promptOutcome{stopReason: stopReasonEndTurn}

		case "stop":
			return promptOutcome{stopReason: stopReasonCancelled}

		case "max_iterations_reached":
			// Deliberately NOT a stopping point — Harness never actually ends
			// the turn here. agent/session.go reserves exactly one more model
			// call after this event fires (requestProgressUpdate) to summarize
			// progress and hand control back to the user, and that summary
			// streams in as ordinary "text" events immediately afterward,
			// followed by a REAL "turn_end". Returning here (as this used to)
			// would cut the pump before any of that arrives — the user would
			// see the warning and then nothing, silently losing exactly the
			// summary this whole mechanism exists to deliver. So: surface the
			// warning as visible chat feedback (same wording as the TUI's) and
			// keep pumping; the eventual stopReason is the normal "end_turn"
			// from the real turn_end below, not a max-iterations-specific one.
			notify(c, sessionID, sessionUpdate{
				SessionUpdate: "agent_message_chunk",
				Content:       ptr(textBlock(fmt.Sprintf("⚠ reached the %d-iteration limit — summarizing progress", evt.MaxIterations))),
			})

		case "error":
			// evt.Details carries the provider's own structured error payload
			// (types.ProviderAPIError.Details — e.g. a rate-limit response
			// body) when the error originated from a provider API call. The
			// TUI already renders this as a pretty-printed JSON block below
			// its error line (internal/transport/tui/events.go); ACP's Error
			// object has an equivalent purpose-built field for exactly this —
			// "data?: unknown" — so the same context reaches Zed instead of
			// being silently dropped down to just the generic message.
			var data any
			if len(evt.Details) > 0 {
				data = evt.Details
			}
			return promptOutcome{err: &rpcError{Code: errCodeInternalError, Message: evt.Message, Data: data}}

		// Every other event type (loop_start/end, tool_args, text_end,
		// thinking_end, turn_start, received_prompt, follow_up_start) is an
		// internal render-control signal with no ACP equivalent — see the
		// design doc's event mapping table.
		default:
		}
	}
	// Channel closed without a terminal event (e.g. the server connection
	// dropped) — treat as a clean end so the client isn't left hanging.
	return promptOutcome{stopReason: stopReasonEndTurn}
}

// notify sends a session/update notification, logging (to stderr, never
// stdout — see jsonrpc.go's stdout discipline note) and swallowing any write
// error: a broken stdout pipe means the client is gone, and there is nothing
// useful left to do for this turn.
func notify(c *conn, sessionID string, u sessionUpdate) {
	_ = c.sendNotification("session/update", sessionUpdateNotification{
		SessionID: sessionID,
		Update:    u,
	})
}

func ptr[T any](v T) *T { return &v }
