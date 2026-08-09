package client

import (
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// This file holds the typed response shapes the client decodes each endpoint
// into, turning this package into a real typed SDK over the API — callers get
// Go values, not map[string]any.
//
// Type-sourcing rule:
//   - Reuse a harness package's own wire type when it's LIGHTWEIGHT (stdlib- or
//     types-only deps) and is exactly what the endpoint serializes, so the
//     contract can't drift: types.* (zero deps), store.SessionMeta (deps types).
//   - Mirror the shape locally when reusing the owner would drag a HEAVY
//     dependency into this HTTP client for nothing: MCPStatus (mcp pulls in the
//     tools + config graph) and MemorySearchResult/Memory (memory pulls the
//     SQLite driver). The client should not import the MCP manager or the memory
//     store just to name a response.
//   - Define a local DTO when the response is a server-only shape with no shared
//     type at all: ServerInfo, Settings, Provider, Model, Session, Schedule,
//     Status, CommandDef/ParamDef.

// ServerInfo is GET /api/server — build/runtime facts about the server process.
type ServerInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// Settings is GET/PATCH /api/settings — the persisted global defaults (the
// active model and thinking level new sessions inherit).
type Settings struct {
	ActiveModel   string `json:"active_model"`
	ThinkingLevel string `json:"thinking_level"`
}

// Provider is one element of GET /api/providers — a registered LLM provider and
// its live activation state.
type Provider struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	Activation  string `json:"activation"`
	// IsSubscription is true for flat-fee subscription providers (e.g. Claude
	// Max) — used to branch connect (OAuth flow vs API-key prompt).
	IsSubscription bool `json:"is_subscription"`
	// CredentialType is the authoritative credential kind the provider needs:
	// "none" (auto-detected), "api_key", or "oauth".
	CredentialType string `json:"credential_type"`
	ModelCount     int    `json:"model_count"`
}

// Model is one element of GET /api/models — a model paired with its provider
// and full capability/pricing metadata (types.ModelMeta, embedded).
type Model struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	IsSubscription bool   `json:"is_subscription"`
	types.ModelMeta
}

// Session is a session document from the session endpoints. It embeds the
// persisted store.SessionMeta and adds MaxIterations — the runtime field the
// server's sessionInfoDTO layers on top for GET/POST /api/sessions and
// resume (the list endpoint omits it, leaving it zero, which list consumers
// don't read).
type Session struct {
	store.SessionMeta
	MaxIterations int `json:"max_iterations,omitempty"`
}

// Schedule is one element of GET /api/schedules — a cron-scheduled prompt in
// the read-only shape the server exposes (mirrors the ScheduleList tool).
type Schedule struct {
	Slug    string `json:"slug"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Runs    int    `json:"runs"`
	LastRun int64  `json:"last_run,omitempty"`
}

// Status is a 2xx action-confirmation body: {"status": {"code", "message"}}.
// Action endpoints (connect, disconnect, close, stop, prompt, exec command)
// return one so callers can confirm the outcome (e.g. code "started" vs
// "queued" for a prompt) rather than sniffing a raw body.
type Status struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// CommandDef and ParamDef mirror GET /api/sessions/{id}/commands — the dynamic
// command set a session accepts (built-ins plus its skills), which the palette
// and command dispatch drive off of.
type CommandDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Params      []ParamDef `json:"params"`
}

type ParamDef struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
}

// ContextBreakdown is GET /api/sessions/{id}/context — estimated token usage
// for the three context components (S/T/C). JSON tags match agent.ContextBreakdown.
type ContextBreakdown struct {
	System       int `json:"system"`       // S: full system prompt
	Tools        int `json:"tools"`        // T: all tool schemas (built-in + MCP)
	Conversation int `json:"conversation"` // C: working-set messages

	EstimatedTotal int `json:"estimated_total"`
	LastRealTotal  int `json:"last_real_total"`
	ContextWindow  int `json:"context_window"`
	FreeSpace      int `json:"free_space"`
}

// SessionInfo is GET /api/sessions/{id}/info — a consolidated, read-only
// snapshot of everything the TUI footer, Telegram /info, and any future
// transport needs in a single round-trip. Only valid for active sessions.
type SessionInfo struct {
	// Version is the harness binary version (e.g. "v0.73.40").
	Version string `json:"version"`
	// Session is the full session metadata plus runtime max_iterations.
	Session Session `json:"session"`
	// Busy is true while the agent is actively processing a turn.
	Busy bool `json:"busy"`
	// QueueDepth is the number of prompts waiting behind the current turn.
	QueueDepth int `json:"queue_depth"`
	// MCPConnected is the number of MCP servers currently connected.
	MCPConnected int `json:"mcp_connected"`
	// ScheduleCount is the number of cron schedules owned by this session
	// (i.e. the schedules that will fire into it).
	ScheduleCount int `json:"schedule_count"`
}

// MCPServer is the settings-collection payload. It reuses types' own shape
// (stdlib-only) verbatim: the endpoints pass it straight through the
// SettingsManager, so this is the same struct end to end.
type MCPServer = types.MCPServer

// MCPStatus is one element of GET /api/mcp/status — a configured MCP server's
// live connection state. Mirrors mcp.Status locally (rather than importing the
// mcp package, which pulls in the tools + config graph) since this HTTP client
// only ever decodes the value, never touches the manager.
type MCPStatus struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"tool_count"`
	Error     string `json:"error,omitempty"`
}

// Memory is one stored memory entry in a MemorySearchResult. Mirrors
// memory.Memory locally (the memory package pulls in the SQLite driver, which
// this client has no need for).
type Memory struct {
	Slug      string  `json:"slug"`
	CWD       string  `json:"cwd,omitempty"`
	Content   string  `json:"content,omitempty"`
	Score     float64 `json:"score,omitempty"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// MemorySearchResult is GET /api/memories — a paginated memory query response.
// Mirrors memory.SearchResult (see Memory for why it's mirrored, not imported).
type MemorySearchResult struct {
	Total    int      `json:"total"`
	Returned int      `json:"returned"`
	Skip     int      `json:"skip"`
	Limit    int      `json:"limit"`
	Results  []Memory `json:"results"`
}
