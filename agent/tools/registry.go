package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// Tool defines a single tool that the agent can use.
// Execute receives a context for cancellation — tools should respect ctx.Done().
type Tool struct {
	Def     types.ToolDef
	Execute func(ctx context.Context, input json.RawMessage) (string, error)
}

// Registry manages available tools.
type Registry struct {
	tools map[string]Tool
	order []string // insertion order for deterministic output
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Def.Name]; !exists {
		r.order = append(r.order, t.Def.Name)
	}
	r.tools[t.Def.Name] = t
}

func (r *Registry) Definitions() []types.ToolDef {
	defs := make([]types.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.tools[name].Def)
	}
	return defs
}

func (r *Registry) Clone() *Registry {
	c := NewRegistry()
	for _, name := range r.order {
		c.tools[name] = r.tools[name]
		c.order = append(c.order, name)
	}
	return c
}

func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Run executes a tool by name, passing ctx for cancellation.
func (r *Registry) Run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		err := fmt.Errorf("unknown tool: %s", name)
		return err.Error(), err
	}
	return t.Execute(ctx, input)
}
