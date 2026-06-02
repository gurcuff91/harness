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
	order []string // insertion order for deterministic output
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Def.Name]; !exists {
		r.order = append(r.order, t.Def.Name)
	}
	r.tools[t.Def.Name] = t
}

// Definitions returns tool schemas in registration order.
func (r *Registry) Definitions() []types.ToolDef {
	defs := make([]types.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.tools[name].Def)
	}
	return defs
}

// Clone returns a shallow copy of the registry preserving order.
func (r *Registry) Clone() *Registry {
	c := NewRegistry()
	for _, name := range r.order {
		c.tools[name] = r.tools[name]
		c.order = append(c.order, name)
	}
	return c
}

// Get returns a tool by name. Returns zero value if not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
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
