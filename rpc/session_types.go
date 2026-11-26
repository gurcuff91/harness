package rpc

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/types"
)

// SessionPromptParams are the params of session/prompt and session/ask: a
// message for an open session. Text or Images is required.
type SessionPromptParams struct {
	// ID is the open session.
	ID   string `json:"id"`
	Text string `json:"text"`
	// Images are attached to the prompt; the session's model must support vision.
	Images []types.ImageData `json:"images,omitempty"`
	// Origin tags where the prompt came from ("user" when empty). Echoed on
	// received_prompt / follow_up_start; never stored.
	Origin string `json:"origin,omitempty"`
	// DisplayText overrides what the prompt echo shows, not what the model gets.
	DisplayText string `json:"display_text,omitempty"`
}

// SessionPromptResult is the result of session/prompt: "started" when the turn
// began at once, "queued" when the session was busy and the prompt waits.
type SessionPromptResult struct {
	Status string `json:"status"`
}

// SessionAskResult is the result of session/ask: the turn's final assistant
// text (empty when the turn was stopped).
type SessionAskResult struct {
	Text string `json:"text"`
}

// SessionSwitchModelParams are the params of session/switch_model.
type SessionSwitchModelParams struct {
	ID string `json:"id"`
	// Model is "provider/model".
	Model string `json:"model"`
}

// SessionSwitchThinkingParams are the params of session/switch_thinking.
type SessionSwitchThinkingParams struct {
	ID string `json:"id"`
	// Level is off | low | medium | high | xhigh | max.
	Level string `json:"level"`
}

// SessionSetMaxIterationsParams are the params of session/set_max_iterations.
type SessionSetMaxIterationsParams struct {
	ID string `json:"id"`
	// N is the per-turn iteration budget, between 1 and 1000.
	N int `json:"n"`
}

// SessionRenameParams are the params of session/rename.
type SessionRenameParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionInfoResult is the result of session/info: the same structure as the
// HTTP GET /api/sessions/{id}/info.
type SessionInfoResult struct {
	// Version is the harness version.
	Version string `json:"version"`
	// Session is the session's detail.
	Session agent.SessionDetail `json:"session"`
	// Busy and QueueDepth are the live runtime state.
	Busy       bool `json:"busy"`
	QueueDepth int  `json:"queue_depth"`
	// MCPConnected counts the connected MCP servers; ScheduleCount the
	// schedules owned by this session.
	MCPConnected  int `json:"mcp_connected"`
	ScheduleCount int `json:"schedule_count"`
	// OS and Arch are the system the session runs on; Terminal is the
	// terminal the agent runs in, when it says so.
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Terminal string `json:"terminal,omitempty"`
}

// SessionSyspromptResult is the result of session/sysprompt.
type SessionSyspromptResult struct {
	// Text is the full system prompt the session sends to the provider.
	Text string `json:"text"`
}

// SessionToolsResult is the result of session/tools.
type SessionToolsResult struct {
	// Tools are the definitions sent to the provider on the next turn.
	Tools []types.ToolDef `json:"tools"`
}

// SessionMessagesParams are the params of session/messages. From and
// Compactions are mutually exclusive; neither = the whole history.
type SessionMessagesParams struct {
	ID string `json:"id"`
	// From starts the window at this absolute index (>= 0).
	From *int `json:"from,omitempty"`
	// Compactions starts the window after the k-th-to-last compaction (k >= 1).
	Compactions *int `json:"compactions,omitempty"`
}

// SessionMessagesResult is the result of session/messages.
type SessionMessagesResult struct {
	Messages []types.Message `json:"messages"`
	// From is the absolute index of the first message returned.
	From int `json:"from"`
	// Total is the length of the whole history.
	Total int `json:"total"`
}

// SessionExecCommandParams are the params of session/exec_command, kept only
// for commands with no method of their own: "skill:<name>" (Params.prompt is
// an optional text added after the skill).
type SessionExecCommandParams struct {
	ID      string         `json:"id"`
	Command string         `json:"command"`
	Params  map[string]any `json:"params,omitempty"`
}

// SessionExecCommandResult is the result of session/exec_command.
type SessionExecCommandResult struct {
	// Status is "started" or "queued" (the skill prompt's status).
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
