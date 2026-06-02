package types

import "encoding/json"

// ── Message — provider-agnostic conversation format ──────────────────────

// MessageRole identifies who sent the message.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

// Message is a single turn in a conversation.
// Stored in the session store in this neutral format — providers translate
// to their own wire format internally before making API calls.
type Message struct {
	Role  MessageRole   `json:"role"`
	Parts []ContentPart `json:"parts"`
}

// ContentPart is one element within a message.
// Exactly one field is non-nil/non-empty per part.
type ContentPart struct {
	Text       string        `json:"text,omitempty"`        // plain text
	Image      *ImageData    `json:"image,omitempty"`       // base64 image (user only)
	Thinking   *ThinkingPart `json:"thinking,omitempty"`    // reasoning block (assistant only)
	ToolCall   *ToolCall     `json:"tool_call,omitempty"`   // tool invocation (assistant only)
	ToolResult *ToolResult   `json:"tool_result,omitempty"` // tool output (user only)
}

// ThinkingPart holds the model's reasoning content.
type ThinkingPart struct {
	Content   string `json:"content"`
	Signature string `json:"signature,omitempty"` // Anthropic cache signature
}

// ── Constructors ──────────────────────────────────────────────────────────

func NewUserTextMessage(text string) Message {
	return Message{
		Role:  RoleUser,
		Parts: []ContentPart{{Text: text}},
	}
}

func NewUserImageMessage(text string, images []ImageData) Message {
	var parts []ContentPart
	for _, img := range images {
		img := img
		parts = append(parts, ContentPart{Image: &img})
	}
	if text != "" {
		parts = append(parts, ContentPart{Text: text})
	}
	return Message{Role: RoleUser, Parts: parts}
}

func NewAssistantTextMessage(text string) Message {
	return Message{
		Role:  RoleAssistant,
		Parts: []ContentPart{{Text: text}},
	}
}

func NewAssistantToolCallMessage(text string, thinking string, thinkingSig string, calls []ToolCall) Message {
	var parts []ContentPart
	if thinking != "" {
		parts = append(parts, ContentPart{Thinking: &ThinkingPart{Content: thinking, Signature: thinkingSig}})
	}
	if text != "" {
		parts = append(parts, ContentPart{Text: text})
	}
	for _, tc := range calls {
		tc := tc
		parts = append(parts, ContentPart{ToolCall: &tc})
	}
	return Message{Role: RoleAssistant, Parts: parts}
}

func NewToolResultMessage(results []ToolResult) Message {
	var parts []ContentPart
	for _, r := range results {
		r := r
		parts = append(parts, ContentPart{ToolResult: &r})
	}
	return Message{Role: RoleUser, Parts: parts}
}

// ── Helpers ───────────────────────────────────────────────────────────────

// IsEmpty returns true if the message has no meaningful content.
func (m Message) IsEmpty() bool {
	return len(m.Parts) == 0
}

// TextContent returns all text parts concatenated.
func (m Message) TextContent() string {
	var s string
	for _, p := range m.Parts {
		s += p.Text
	}
	return s
}

// ToolCalls returns all tool call parts.
func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, p := range m.Parts {
		if p.ToolCall != nil {
			calls = append(calls, *p.ToolCall)
		}
	}
	return calls
}

// MarshalJSON implements json.Marshaler — compact wire format.
func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role  MessageRole   `json:"role"`
		Parts []ContentPart `json:"parts"`
	}
	return json.Marshal(wire{Role: m.Role, Parts: m.Parts})
}
