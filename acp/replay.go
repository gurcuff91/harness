package acp

import (
	"github.com/gurcuff91/harness/types"
)

// replayHistory converts a session's stored message log into an ordered
// sequence of "session/update" notifications, sent during session/load so
// the ACP client can reconstruct the conversation exactly as Harness has it.
// All notifications here MUST be sent before the session/load response, per
// spec (agentclientprotocol.com/protocol/v1/session-setup).
//
// Tool calls already ran to completion when they were recorded, so each one
// replays as a SINGLE tool_call notification carrying its final state
// directly (title/kind/status/content all at once) — there is no
// pending → in_progress → completed dance to replay, unlike the live path in
// events.go where those phases happen in real time.
//
// A ToolCall (in an assistant message) and its ToolResult (in the following
// user message, per types.NewToolResultMessage) are correlated by ID — the
// result carries the output text, the call carries the name (for kind/title)
// and input (for rawInput). Results are indexed first in one pass so the
// second pass can emit a complete tool_call as soon as it sees the call.
func replayHistory(c *conn, sessionID string, messages []types.Message) {
	results := map[string]types.ToolResult{}
	for _, m := range messages {
		for _, p := range m.Parts {
			if p.ToolResult != nil {
				results[p.ToolResult.ID] = *p.ToolResult
			}
		}
	}

	for _, m := range messages {
		for _, p := range m.Parts {
			switch {
			case p.Text != "" && m.Role == types.RoleUser:
				notify(c, sessionID, sessionUpdate{
					SessionUpdate: "user_message_chunk",
					Content:       ptr(textBlock(p.Text)),
				})

			case p.Text != "" && m.Role == types.RoleAssistant:
				notify(c, sessionID, sessionUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       ptr(textBlock(p.Text)),
				})

			case p.Thinking != nil:
				notify(c, sessionID, sessionUpdate{
					SessionUpdate: "agent_thought_chunk",
					Content:       ptr(textBlock(p.Thinking.Content)),
				})

			case p.ToolCall != nil:
				call := p.ToolCall
				status := toolStatusCompleted
				var content []toolCallContent
				if res, ok := results[call.ID]; ok {
					if res.IsErr {
						status = toolStatusFailed
					}
					content = contentOnly(res.Output)
				}
				notify(c, sessionID, sessionUpdate{
					SessionUpdate: "tool_call",
					ToolCallID:    call.ID,
					Title:         call.Name,
					Kind:          toolKind(call.Name),
					Status:        status,
					ToolContent:   content,
					RawInput:      call.Input,
				})

			case p.ToolResult != nil:
				// Already folded into the matching tool_call above — nothing
				// further to emit for this part on its own.
			}
		}
	}
}
