package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// Client is an MCP client bound to a single server over a Transport. It handles
// the initialize handshake and the tools primitive (list + call). Phase 1 only.
type Client struct {
	transport Transport
	nextID    atomic.Int64
}

// NewClient wraps a Transport. Call Initialize before ListTools/CallTool.
func NewClient(t Transport) *Client {
	return &Client{transport: t}
}

// Initialize performs the MCP handshake: the initialize request followed by the
// notifications/initialized notification. Must be called once before any other
// method.
func (c *Client) Initialize(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    map[string]any{}, // client advertises no special capabilities in phase 1
		ClientInfo:      implementation{Name: clientName, Version: clientVersion},
	}
	req, err := newRequest(c.nextID.Add(1), "initialize", params)
	if err != nil {
		return err
	}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize: %s", resp.Error.Message)
	}
	var result initializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("initialize: decode result: %w", err)
	}

	// Confirm readiness with the initialized notification.
	note, err := newNotification("notifications/initialized", nil)
	if err != nil {
		return err
	}
	if err := c.transport.Notify(ctx, note); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}
	return nil
}

// ListTools fetches the server's advertised tools via tools/list.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	req, err := newRequest(c.nextID.Add(1), "tools/list", struct{}{})
	if err != nil {
		return nil, err
	}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list: %s", resp.Error.Message)
	}
	var result listToolsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("tools/list: decode: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool by name with JSON arguments via tools/call. A nil or
// empty args is sent as an empty object.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	req, err := newRequest(c.nextID.Add(1), "tools/call", callToolParams{Name: name, Arguments: args})
	if err != nil {
		return CallResult{}, err
	}
	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return CallResult{}, fmt.Errorf("tools/call %q: %w", name, err)
	}
	if resp.Error != nil {
		return CallResult{}, fmt.Errorf("tools/call %q: %s", name, resp.Error.Message)
	}
	var result CallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return CallResult{}, fmt.Errorf("tools/call %q: decode: %w", name, err)
	}
	return result, nil
}

// Close terminates the underlying transport (and subprocess for stdio).
func (c *Client) Close() error {
	return c.transport.Close()
}
