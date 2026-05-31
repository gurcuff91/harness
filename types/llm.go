package types

// ── LLM request/response types ───────────────────────────────────────────

// ImageData holds a base64-encoded image for vision requests.
type ImageData struct {
	MimeType string
	Base64   string
}

// Request represents an LLM completion request.
type Request struct {
	Model         string    `json:"model"`
	SystemPrompt  string    `json:"system_prompt"`
	Messages      []Message `json:"messages"` // provider-agnostic — translated internally
	Tools         []ToolDef `json:"tools,omitempty"`
	MaxTokens     int       `json:"max_tokens"`
	ThinkingLevel string    `json:"thinking_level,omitempty"` // disable|low|medium|high|xhigh
}

// Response represents an LLM completion response.
type Response struct {
	Text      string     `json:"text"`
	Message   Message    `json:"message"` // the assistant message to add to history
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage      `json:"usage"`
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read_input_tokens,omitempty"`
	CacheWrite   int `json:"cache_creation_input_tokens,omitempty"`
}
