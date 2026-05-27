package tools

import (
	"encoding/json"
	"fmt"

	llm "github.com/gurcuff91/harness/providers/llm"
)

// Tool defines a single tool that the agent can use.
type Tool struct {
	Def     llm.ToolDef
	Execute func(input json.RawMessage) (string, error)
}

// Registry manages available tools.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Def.Name] = t
}

// Definitions returns all tool schemas for the LLM.
func (r *Registry) Definitions() []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Def)
	}
	return defs
}

// Run executes a tool by name with the given input.
func (r *Registry) Run(name string, input json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(input)
}
