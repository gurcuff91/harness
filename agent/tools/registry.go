package tools

import (
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// Tool defines a single tool that the agent can use.
// Execute returns (string, error):
//   - string: always sent to the LLM (even on error)
//   - error: Go-level signal — used to set IsError on the event/result
type Tool struct {
	Def     types.ToolDef
	Execute func(input json.RawMessage) (string, error)
}

// Registry manages available tools.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Def.Name] = t
}

// Definitions returns all tool schemas for the LLM.
func (r *Registry) Definitions() []types.ToolDef {
	defs := make([]types.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Def)
	}
	return defs
}

// Clone returns a shallow copy of the registry.
func (r *Registry) Clone() *Registry {
	c := NewRegistry()
	for k, v := range r.tools {
		c.tools[k] = v
	}
	return c
}

// Run executes a tool by name with the given input.
// Returns (text, error) — text always goes to the LLM, error signals failure.
func (r *Registry) Run(name string, input json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		err := fmt.Errorf("unknown tool: %s", name)
		return err.Error(), err
	}
	return t.Execute(input)
}
