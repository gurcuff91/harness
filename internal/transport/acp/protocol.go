package acp

import "encoding/json"

// protocolVersion is the ACP major version this transport implements —
// v1, the stable spec (NOT v2, which is still Draft as of this writing).
const protocolVersion = 1

// ── initialize ─────────────────────────────────────────────────────────────

type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         *implementation    `json:"clientInfo,omitempty"`
}

type clientCapabilities struct {
	FS       fsCapabilities `json:"fs,omitempty"`
	Terminal bool           `json:"terminal,omitempty"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AgentInfo         *implementation   `json:"agentInfo,omitempty"`
	AuthMethods       []authMethod      `json:"authMethods,omitempty"`
}

type agentCapabilities struct {
	LoadSession         bool                `json:"loadSession,omitempty"`
	PromptCapabilities  promptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities sessionCapabilities `json:"sessionCapabilities,omitempty"`
}

type promptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

// sessionCapabilities advertises optional session lifecycle methods this
// agent supports. Each field is an empty object (not just `true`) per spec —
// future sub-options would live inside; omitted entirely means unsupported.
// Harness's client.Client already exposes Resume/Close/Delete/List for every
// transport (not ACP-specific), so all four are genuinely backed here, not
// just advertised.
type sessionCapabilities struct {
	Resume *struct{} `json:"resume,omitempty"`
	Close  *struct{} `json:"close,omitempty"`
	Delete *struct{} `json:"delete,omitempty"`
	List   *struct{} `json:"list,omitempty"`
}

type authMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ── authenticate ───────────────────────────────────────────────────────────

type authenticateParams struct {
	MethodID string `json:"methodId"`
}

// ── session/new, session/load ───────────────────────────────────────────────

type mcpServerStdio struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type newSessionParams struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"` // deliberately ignored — see design doc
}

type loadSessionParams struct {
	SessionID  string            `json:"sessionId"`
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"`
}

type newSessionResult struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []sessionConfigOption `json:"configOptions,omitempty"`
}

// ── session/set_config_option ───────────────────────────────────────────────

// setConfigOptionParams' Value is deliberately untyped (json.RawMessage):
// ACP's "value" field is `string | boolean` depending on the option's type —
// this transport only ever advertises "select" options (string values), so
// it unmarshals Value as a plain string in methods.go and rejects anything
// else, rather than modeling the boolean variant this transport never sends.
type setConfigOptionParams struct {
	SessionID string          `json:"sessionId"`
	ConfigID  string          `json:"configId"`
	Value     json.RawMessage `json:"value"`
}

// setConfigOptionResult carries back the COMPLETE, current config option
// state — per spec, not just the one that changed (a model change can shift
// what's available/current for another option).
type setConfigOptionResult struct {
	ConfigOptions []sessionConfigOption `json:"configOptions"`
}

// ── session/prompt ───────────────────────────────────────────────────────

type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

// StopReason values (ACP v1 schema).
const (
	stopReasonEndTurn         = "end_turn"
	stopReasonMaxTokens       = "max_tokens"
	stopReasonMaxTurnRequests = "max_turn_requests"
	stopReasonRefusal         = "refusal"
	stopReasonCancelled       = "cancelled"
)

// ── session/cancel ─────────────────────────────────────────────────────────

type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// ── session/resume ───────────────────────────────────────────────────────

// resumeSessionParams mirrors loadSessionParams (same fields, same MCP
// servers-ignored rule) — session/resume is the lighter-weight sibling of
// session/load: it reconnects without replaying history.
type resumeSessionParams struct {
	SessionID  string            `json:"sessionId"`
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"`
}

// resumeSessionResult reuses newSessionResult's shape (sessionId +
// configOptions) — ACP's ResumeSessionResponse has the identical fields.
type resumeSessionResult = newSessionResult

// ── session/close ────────────────────────────────────────────────────────

type closeSessionParams struct {
	SessionID string `json:"sessionId"`
}

// closeSessionResult is intentionally empty — ACP's CloseSessionResponse
// carries no fields besides the reserved _meta this transport never sets.
type closeSessionResult struct{}

// ── session/delete ───────────────────────────────────────────────────────

type deleteSessionParams struct {
	SessionID string `json:"sessionId"`
}

type deleteSessionResult struct{}

// ── session/list ─────────────────────────────────────────────────────────

type listSessionsParams struct {
	CWD    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type listSessionsResult struct {
	Sessions   []sessionInfo `json:"sessions"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// sessionInfo is ACP's SessionInfo — only sessionId and cwd are required;
// this transport also fills title (Harness's session name, e.g. "Acp
// 2026-08-01 18:30") and updatedAt (RFC3339, matching ACP's ISO 8601
// expectation) from client.Session's embedded store.SessionMeta.
type sessionInfo struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ── Content blocks (shared with MCP's ContentBlock — see agentclientprotocol.com/protocol/v1/content) ──

// contentBlock is a tagged union over "type": text | image | audio |
// resource | resource_link. Only the fields relevant to the type are
// populated; the rest stay zero. This transport reads text/image/resource
// (embeddedContext) from prompts and only ever WRITES type "text" itself.
type contentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image / audio
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`

	// resource (embedded context) — the content is embedded directly by the
	// client, no path resolution needed on our side.
	Resource *embeddedResource `json:"resource,omitempty"`

	// resource_link — reference only, no content attached; not read by this
	// transport (see design doc: out of scope for the first cut).
	URI  string `json:"uri,omitempty"`
	Name string `json:"name,omitempty"`
}

type embeddedResource struct {
	URI      string `json:"uri"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

func textBlock(text string) contentBlock {
	return contentBlock{Type: "text", Text: text}
}

// ── session/update ─────────────────────────────────────────────────────────

type sessionUpdateNotification struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

// sessionUpdate is a tagged union over "sessionUpdate". Only one shape is
// populated per instance, chosen by the constructor helpers in events.go.
// It marshals itself manually (see MarshalJSON) because the "content" key
// means two different things depending on the variant — a single
// ContentBlock for message chunks, or a []toolCallContent for tool calls —
// which Go can't express as two struct fields sharing one JSON tag.
type sessionUpdate struct {
	SessionUpdate string `json:"-"`

	// user_message_chunk / agent_message_chunk / agent_thought_chunk
	Content *contentBlock `json:"-"`

	// tool_call / tool_call_update
	ToolCallID  string             `json:"-"`
	Title       string             `json:"-"`
	Kind        string             `json:"-"`
	Status      string             `json:"-"`
	ToolContent []toolCallContent  `json:"-"`
	Locations   []toolCallLocation `json:"-"`
	RawInput    json.RawMessage    `json:"-"`

	// available_commands_update
	AvailableCommands []availableCommand `json:"-"`

	// config_option_update
	ConfigOptions []sessionConfigOption `json:"-"`

	// usage_update
	Used int64      `json:"-"`
	Size int64      `json:"-"`
	Cost *usageCost `json:"-"`
}

// MarshalJSON builds the wire object by hand, including only the fields that
// variant actually carries — see the type comment above for why.
func (u sessionUpdate) MarshalJSON() ([]byte, error) {
	m := map[string]any{"sessionUpdate": u.SessionUpdate}
	switch u.SessionUpdate {
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk":
		m["content"] = u.Content
	case "tool_call", "tool_call_update":
		if u.ToolCallID != "" {
			m["toolCallId"] = u.ToolCallID
		}
		if u.Title != "" {
			m["title"] = u.Title
		}
		if u.Kind != "" {
			m["kind"] = u.Kind
		}
		if u.Status != "" {
			m["status"] = u.Status
		}
		if u.ToolContent != nil {
			m["content"] = u.ToolContent
		}
		if u.Locations != nil {
			m["locations"] = u.Locations
		}
		if u.RawInput != nil {
			m["rawInput"] = u.RawInput
		}
	case "available_commands_update":
		m["availableCommands"] = u.AvailableCommands
	case "config_option_update":
		m["configOptions"] = u.ConfigOptions
	case "usage_update":
		m["used"] = u.Used
		m["size"] = u.Size
		if u.Cost != nil {
			m["cost"] = u.Cost
		}
	}
	return json.Marshal(m)
}

// ToolKind values (ACP v1 schema).
const (
	toolKindRead   = "read"
	toolKindEdit   = "edit"
	toolKindDelete = "delete"
	toolKindMove   = "move"
	toolKindSearch = "search"
	toolKindExec   = "execute"
	toolKindThink  = "think"
	toolKindFetch  = "fetch"
	toolKindOther  = "other"
)

// ToolCallStatus values (ACP v1 schema).
const (
	toolStatusPending    = "pending"
	toolStatusInProgress = "in_progress"
	toolStatusCompleted  = "completed"
	toolStatusFailed     = "failed"
)

// toolCallContent is a tagged union over "type": content | diff | terminal.
// This transport only ever produces "content" (plain text) and "diff" (for
// the Edit tool, built observationally in diff.go) — "terminal" is unused
// since terminal delegation is out of scope.
type toolCallContent struct {
	Type string `json:"type"`

	// content
	Content *contentBlock `json:"content,omitempty"`

	// diff
	Path    string  `json:"path,omitempty"`
	OldText *string `json:"oldText"` // present (possibly null) only on diff blocks
	NewText string  `json:"newText,omitempty"`
}

func contentOnly(text string) []toolCallContent {
	c := textBlock(text)
	return []toolCallContent{{Type: "content", Content: &c}}
}

type toolCallLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

type availableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *availableCommandInput `json:"input,omitempty"`
}

type availableCommandInput struct {
	Hint string `json:"hint"`
}

// ── Session Config Options ──────────────────────────────────────────────────

// sessionConfigOption is ACP's ConfigOption — note the wire field is
// "currentValue", NOT "value" (that name is reserved for
// ConfigOptionValue.Value, the id of one entry in Options). Getting this
// wrong doesn't error — Zed (and presumably any client) just silently
// declines to render a selector for an option it can't find a current value
// for, which is exactly the bug this comment is here to prevent regressing.
type sessionConfigOption struct {
	ID           string                     `json:"id"`
	Category     string                     `json:"category,omitempty"`
	Name         string                     `json:"name"`
	Type         string                     `json:"type"` // "select" | "boolean"
	CurrentValue string                     `json:"currentValue"`
	Options      []sessionConfigOptionValue `json:"options,omitempty"`
}

type sessionConfigOptionValue struct {
	Value string `json:"value"`
	Name  string `json:"name,omitempty"`
}

type usageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}
