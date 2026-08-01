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
type sessionCapabilities struct {
	Resume *struct{} `json:"resume,omitempty"`
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

type sessionConfigOption struct {
	ID       string                     `json:"id"`
	Category string                     `json:"category,omitempty"`
	Name     string                     `json:"name"`
	Type     string                     `json:"type"` // "select" | "boolean"
	Value    string                     `json:"value,omitempty"`
	Options  []sessionConfigOptionValue `json:"options,omitempty"`
}

type sessionConfigOptionValue struct {
	Value string `json:"value"`
	Name  string `json:"name,omitempty"`
}

type usageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}
