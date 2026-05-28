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
	ID     string `json:"id"`
	Output string `json:"output"`
	IsErr  bool   `json:"is_error,omitempty"`
}
