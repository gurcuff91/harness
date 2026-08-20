// Package types defines core data types shared across all harness modules.
// This package imports only stdlib — it is the foundation of the dependency graph.
package types

import "encoding/json"

// ── Tool types ───────────────────────────────────────────────────────────

// ToolDef defines a tool's schema for the LLM.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult represents the output of a tool execution.
type ToolResult struct {
	ID     string      `json:"id"`
	Output string      `json:"output"`
	Images []ImageData `json:"images,omitempty"` // optional image content blocks
	IsErr  bool        `json:"is_error,omitempty"`
}

// RawWrapperKey is the sole field name of the wrapper a provider streaming
// layer (internal/providers/llm/{anthropic,openai}.go) uses when a model
// streams malformed JSON as tool-call arguments: {"<RawWrapperKey>": "<the
// raw, invalid JSON as an escaped string>"}. json.Valid rejects the raw
// content outright, but the ToolCall.Input field must still be valid JSON
// itself (store.AddMessage marshals the whole message), so the invalid
// content is wrapped as a JSON string value instead of being dropped or
// panicking.
//
// Lives here (types, imported by both agent/tools and internal/providers/llm)
// rather than in either package directly — agent/ and internal/providers/
// deliberately never import each other (see AGENTS.md's backend/frontend
// separation rule), so the one constant both sides need to agree on belongs
// in the shared, dependency-free types package.
//
// The leading AND trailing underscore make an accidental collision with a
// real tool's own field name unlikely (vs. a bare "raw"), while
// IsRawWrapper's structural check (exactly one key, string-typed value) is
// the actual safeguard against misidentifying a legitimate tool call as this
// wrapper.
const RawWrapperKey = "_raw_"

// IsRawWrapper reports whether input is the RawWrapperKey wrapper describing
// malformed tool-call arguments the model streamed, returning the raw
// (invalid) content that was wrapped. ok is false for any normal, well-formed
// tool call — including one that coincidentally has a single field, as long
// as it isn't named RawWrapperKey with a string value.
func IsRawWrapper(input json.RawMessage) (raw string, ok bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(input, &probe); err != nil || len(probe) != 1 {
		return "", false
	}
	v, exists := probe[RawWrapperKey]
	if !exists {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}
