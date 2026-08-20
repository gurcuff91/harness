package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// Tool defines a single tool that the agent can use.
// Execute receives a context for cancellation — tools should respect ctx.Done().
// ExecuteRich is an optional richer variant that can also return images.
// If ExecuteRich is set it takes priority over Execute.
type Tool struct {
	Def         types.ToolDef
	Execute     func(ctx context.Context, input json.RawMessage) (string, error)
	ExecuteRich func(ctx context.Context, input json.RawMessage) (string, []types.ImageData, error)
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
// Returns (output, images, error). Images is non-nil only for vision-capable tools.
//
// Before dispatching to the tool's own Execute/ExecuteRich, it checks whether
// input is a types.RawWrapperKey wrapper — the provider streaming layer's
// marker for "the model streamed malformed JSON as this call's arguments"
// (see internal/providers/llm/{anthropic,openai}.go). Without this check,
// {"_raw_": "<invalid JSON as a string>"} unmarshals successfully into any
// tool's typed input struct (unknown JSON fields are silently ignored), so
// every declared field reads as its zero value and the tool fails with a
// misleading "missing required field: X" — as if the model had simply
// omitted a field, when it actually emitted unparseable JSON. Centralized
// here (rather than duplicated in every tool's Execute) so every tool, built-in
// or MCP or SDK-custom, gets the correct diagnosis automatically.
func (r *Registry) Run(ctx context.Context, name string, input json.RawMessage) (string, []types.ImageData, error) {
	t, ok := r.tools[name]
	if !ok {
		err := fmt.Errorf("unknown tool: %s", name)
		return err.Error(), nil, err
	}
	if _, wrapped := types.IsRawWrapper(input); wrapped {
		err := fmt.Errorf("invalid JSON in tool-call arguments — retry this call with a single, well-formed JSON object")
		return err.Error(), nil, err
	}
	if t.ExecuteRich != nil {
		return t.ExecuteRich(ctx, input)
	}
	out, err := t.Execute(ctx, input)
	return out, nil, err
}
