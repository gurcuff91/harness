package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/types"
)

// bridgeTool turns a declaration into a tool whose Execute asks the client.
func (s *Server) bridgeTool(d ToolDecl) tools.Tool {
	schema := d.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	run := func(ctx context.Context, input json.RawMessage) (string, []types.ImageData, error) {
		return s.callTool(ctx, d.Name, input)
	}
	return tools.Tool{
		Def: types.ToolDef{Name: d.Name, Description: d.Description, InputSchema: schema},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			out, _, err := run(ctx, input)
			return out, err
		},
		ExecuteRich: run,
	}
}

// bridgeTools validates and bridges a list of declarations.
func (s *Server) bridgeTools(ds []ToolDecl) ([]tools.Tool, error) {
	out := make([]tools.Tool, 0, len(ds))
	for _, d := range ds {
		if err := validateToolDecl(d); err != nil {
			return nil, err
		}
		out = append(out, s.bridgeTool(d))
	}
	return out, nil
}

func validateToolDecl(d ToolDecl) error {
	if d.Name == "" {
		return errInvalid("a declared tool needs a name")
	}
	if len(d.InputSchema) > 0 && !json.Valid(d.InputSchema) {
		return errInvalid("tool %q: input_schema is not valid JSON", d.Name)
	}
	return nil
}

// callTool is the tool callback: a `tool/call` request to the client, awaited.
// A cancelled ctx (session/stop, shutdown) sends `tool/cancel` and returns the
// ctx error; the client's late answer is dropped by the connection. A closed
// connection makes the tool fail so the turn can go on.
func (s *Server) callTool(ctx context.Context, name string, input json.RawMessage) (string, []types.ImageData, error) {
	sessionID, _ := agent.SessionID(ctx)
	callID := uuid.NewString()
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	raw, err := s.conn.Call(ctx, "tool/call", ToolCallParams{SessionID: sessionID, ToolCallID: callID, Name: name, Input: input})
	if err != nil {
		if ctx.Err() != nil {
			_ = s.conn.Notify("tool/cancel", ToolCancelParams{ToolCallID: callID})
			return "", nil, ctx.Err()
		}
		var je *jsonrpc.Error
		switch {
		case errors.As(err, &je):
			return "", nil, fmt.Errorf("tool %s: client error %d: %s", name, je.Code, je.Message)
		case errors.Is(err, jsonrpc.ErrClosed):
			return "", nil, fmt.Errorf("tool %s: the client disconnected", name)
		}
		return "", nil, fmt.Errorf("tool %s: %w", name, err)
	}
	var res ToolCallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", nil, fmt.Errorf("tool %s: invalid tool/call result: %w", name, err)
	}
	if res.IsError {
		return res.Content, nil, errors.New(res.Content)
	}
	return res.Content, res.Images, nil
}
