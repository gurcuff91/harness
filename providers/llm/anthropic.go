package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gurcuff91/harness/types"
)

// ── AnthropicTool ────────────────────────────────────────────────────────

type AnthropicTool struct {
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	InputSchema        json.RawMessage `json:"input_schema"`
	EagerInputStreaming bool            `json:"eager_input_streaming,omitempty"`
	CacheControl       *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// AnthropicCacheControl marks content blocks for Anthropic prompt caching.
type AnthropicCacheControl struct {
	Type string `json:"type"`
}

// Keep unexported alias for internal use
type anthropicCache = AnthropicCacheControl

// ── AnthropicRequest ─────────────────────────────────────────────────────

// AnthropicRequest extends types.Request with Anthropic-specific fields.
// Built by each Anthropic provider before calling DoAnthropicStream.
// Allows providers to pre-resolve thinking config and wire messages using
// authoritative data (ModelMeta, OAuth stealth, cache_control, etc.)
// while sharing a single streaming implementation.
type AnthropicRequest struct {
	*types.Request

	// Pre-resolved thinking config from ModelMeta.
	// nil = DoAnthropicStream resolves internally via string heuristic.
	ThinkingConfig *ThinkingConfig

	// Pre-built wire messages (Anthropic JSON format).
	// nil = DoAnthropicStream builds from Request.Messages via TranslateMessageToAnthropic.
	WireMessages []json.RawMessage

	// Pre-built system blocks (supports cache_control, multiple blocks, etc.)
	// nil = DoAnthropicStream uses Request.SystemPrompt as plain string.
	SystemBlocks []map[string]any

	// Pre-built tools (supports cache_control, eager_input_streaming, name mapping, etc.)
	// nil = DoAnthropicStream builds from Request.Tools with AnthropicTool defaults.
	Tools []AnthropicTool

	// UnmapTool reverses tool name mapping on response (e.g. MCP stealth).
	// nil = no mapping (tool names passed through as-is).
	UnmapTool func(string) string
}

// ── DoAnthropicStream ────────────────────────────────────────────────────

// DoAnthropicStream sends a streaming request to the Anthropic Messages API.
// Accepts an AnthropicRequest that may carry pre-built wire messages, system blocks,
// tools, and thinking config — allowing each provider to customize without
// duplicating the HTTP + SSE parsing logic.
func DoAnthropicStream(ctx context.Context, client *http.Client, apiURL, apiKey string,
	req *AnthropicRequest, extraHeaders map[string]string,
	cb types.StreamCallback) (*types.Response, error) {

	// ── Thinking config ──────────────────────────────────────────────────
	var thinkingFull ThinkingConfig
	if req.ThinkingConfig != nil {
		thinkingFull = *req.ThinkingConfig // authoritative — from ModelMeta
	} else {
		thinkingFull, _ = BuildAnthropicThinkingFull(req.Model, req.ThinkingLevel, req.MaxTokens)
	}

	// ── Tools ────────────────────────────────────────────────────────────
	tools := req.Tools
	if tools == nil {
		tools = defaultAnthropicTools(req.Request.Tools)
	}

	// ── System ───────────────────────────────────────────────────────────
	var system any
	if req.SystemBlocks != nil {
		system = req.SystemBlocks
	} else {
		system = req.SystemPrompt
	}

	// ── Wire messages ────────────────────────────────────────────────────
	wireMsgs := req.WireMessages
	if wireMsgs == nil {
		wireMsgs = make([]json.RawMessage, 0, len(req.Messages))
		for _, m := range req.Messages {
			for _, w := range TranslateMessageToAnthropic(m) {
				wireMsgs = append(wireMsgs, w)
			}
		}
	}

	// ── Build body ───────────────────────────────────────────────────────
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": thinkingFull.MaxTokens,
		"system":     system,
		"messages":   wireMsgs,
		"tools":      tools,
		"stream":     true,
		"thinking":   thinkingFull.Thinking,
	}
	if thinkingFull.OutputConfig != nil {
		body["output_config"] = thinkingFull.OutputConfig
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// ── HTTP request ─────────────────────────────────────────────────────
	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	// Apply extra headers first — they may override defaults (e.g. OAuth uses Authorization instead of x-api-key)
	for k, v := range extraHeaders {
		httpReq.Header.Set(k, v)
	}
	// Only set x-api-key if Authorization is not already set (OAuth providers set Authorization: Bearer)
	if httpReq.Header.Get("Authorization") == "" && apiKey != "" {
		httpReq.Header.Set("x-api-key", apiKey)
	}

	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic API error %d: %s", httpResp.StatusCode, string(b))
	}

	unmapTool := req.UnmapTool
	if unmapTool == nil {
		unmapTool = func(s string) string { return s }
	}
	return ParseAnthropicStream(httpResp.Body, cb, unmapTool)
}

// defaultAnthropicTools converts types.ToolDef slice to AnthropicTool slice.
func defaultAnthropicTools(defs []types.ToolDef) []AnthropicTool {
	tools := make([]AnthropicTool, len(defs))
	for i, t := range defs {
		tools[i] = AnthropicTool{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		}
	}
	return tools
}

func ParseAnthropicStream(body io.Reader, cb types.StreamCallback, unmapTool func(string) string) (*types.Response, error) {
	emit := func(e types.StreamEvent) {
		if cb != nil { cb(e) }
	}

	resp := &types.Response{}
	var thinkingBuf string   // accumulated thinking content
	var lastThinkingSig string // last thinking block signature

	type blockState struct {
		blockType string
		text      string
		thinking  string
		signature string
		toolID    string
		toolName  string
		toolJSON  string
	}
	blocks := make(map[int]*blockState)

	for sse := range ParseSSE(body) {
		if sse.Event == "error" {
			emit(types.StreamEvent{Type: types.StreamError, Delta: sse.Data})
			return nil, fmt.Errorf("stream error: %s", sse.Data)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(sse.Data), &event); err != nil {
			continue
		}

		switch event["type"].(string) {
		case "message_start":
			if msg, ok := event["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					resp.Usage.InputTokens = jsonInt(u, "input_tokens")
					resp.Usage.OutputTokens = jsonInt(u, "output_tokens")
					resp.Usage.CacheRead = jsonInt(u, "cache_read_input_tokens")
					resp.Usage.CacheWrite = jsonInt(u, "cache_creation_input_tokens")
				}
			}
		case "content_block_start":
			idx := jsonInt(event, "index")
			cb2, _ := event["content_block"].(map[string]any)
			bt, _ := cb2["type"].(string)
			bs := &blockState{blockType: bt}
			blocks[idx] = bs
			switch bt {
			case "tool_use":
				bs.toolID, _ = cb2["id"].(string)
				bs.toolName = unmapTool(jsonStr(cb2, "name"))
				emit(types.StreamEvent{Type: types.StreamToolStart, ToolID: bs.toolID, ToolName: bs.toolName})
			case "thinking":
				// Adaptive thinking may deliver content in block_start (summarized display)
				if t, ok := cb2["thinking"].(string); ok && t != "" {
					bs.thinking = t
					emit(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: t})
				}
				if sig, ok := cb2["signature"].(string); ok {
					bs.signature = sig
				}
			case "redacted_thinking":
				// Adaptive thinking may return redacted blocks (signature only)
				bs.blockType = "thinking" // treat as thinking for stop handling
				if sig, ok := cb2["data"].(string); ok {
					bs.signature = sig
				}
				// Emit a minimal thinking delta so the renderer shows the thinking indicator
				emit(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: "[thinking]"})
			}
		case "content_block_delta":
			idx := jsonInt(event, "index")
			bs := blocks[idx]
			if bs == nil { continue }
			delta, _ := event["delta"].(map[string]any)
			switch delta["type"].(string) {
			case "text_delta":
				text, _ := delta["text"].(string)
				bs.text += text
				emit(types.StreamEvent{Type: types.StreamTextDelta, Delta: text})
			case "thinking_delta":
				thinking, _ := delta["thinking"].(string)
				bs.thinking += thinking
				emit(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: thinking})
			case "signature_delta":
				sig, _ := delta["signature"].(string)
				bs.signature += sig
			case "input_json_delta":
				partial, _ := delta["partial_json"].(string)
				bs.toolJSON += partial
				emit(types.StreamEvent{Type: types.StreamToolDelta, Delta: partial})
			}
		case "content_block_stop":
			idx := jsonInt(event, "index")
			bs := blocks[idx]
			if bs == nil { continue }
			switch bs.blockType {
			case "text":
				if resp.Text != "" { resp.Text += "\n" }
				resp.Text += bs.text
			case "thinking":
				if thinkingBuf != "" { thinkingBuf += "\n" }
				thinkingBuf += bs.thinking
				if bs.signature != "" {
					lastThinkingSig = bs.signature
				}
			case "tool_use":
				input := json.RawMessage(bs.toolJSON)
				if len(input) == 0 { input = json.RawMessage("{}") }
				resp.ToolCalls = append(resp.ToolCalls, types.ToolCall{
					ID: bs.toolID, Name: bs.toolName, Input: input,
				})
				emit(types.StreamEvent{Type: types.StreamToolEnd, ToolID: bs.toolID, ToolName: bs.toolName, ToolArgs: input})
			}
			delete(blocks, idx)
		case "message_delta":
			if u, ok := event["usage"].(map[string]any); ok {
				resp.Usage.OutputTokens = jsonInt(u, "output_tokens")
				resp.Usage.CacheRead = jsonInt(u, "cache_read_input_tokens")
				resp.Usage.CacheWrite = jsonInt(u, "cache_creation_input_tokens")
			}
		case "message_stop":
			emit(types.StreamEvent{Type: types.StreamUsage, InputTokens: resp.Usage.InputTokens,
				OutputTokens: resp.Usage.OutputTokens, CacheRead: resp.Usage.CacheRead,
				CacheWrite: resp.Usage.CacheWrite})
			emit(types.StreamEvent{Type: types.StreamDone})
		}
	}

	resp.Message = types.NewAssistantToolCallMessage(resp.Text, thinkingBuf, lastThinkingSig, resp.ToolCalls)
	return resp, nil
}

// TranslateMessageToAnthropic converts a types.Message to Anthropic wire format.
func TranslateMessageToAnthropic(msg types.Message) []json.RawMessage {
	switch msg.Role {
	case types.RoleUser:
		var content []map[string]any
		for _, p := range msg.Parts {
			if p.ToolResult != nil {
				block := map[string]any{
					"type": "tool_result", "tool_use_id": p.ToolResult.ID, "content": p.ToolResult.Output,
				}
				if p.ToolResult.IsErr {
					block["is_error"] = true
				}
				content = append(content, block)
			} else if p.Image != nil {
				content = append(content, map[string]any{
					"type": "image",
					"source": map[string]string{"type": "base64", "media_type": p.Image.MimeType, "data": p.Image.Base64},
				})
			} else if p.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": p.Text})
			}
		}
		d, _ := json.Marshal(map[string]any{"role": "user", "content": content})
		return []json.RawMessage{d}

	case types.RoleAssistant:
		var content []map[string]any
		for _, p := range msg.Parts {
			if p.Thinking != nil {
				block := map[string]any{"type": "thinking", "thinking": p.Thinking.Content}
				if p.Thinking.Signature != "" {
					block["signature"] = p.Thinking.Signature
				}
				content = append(content, block)
			} else if p.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": p.Text})
			} else if p.ToolCall != nil {
				content = append(content, map[string]any{
					"type": "tool_use", "id": p.ToolCall.ID,
					"name": p.ToolCall.Name, "input": json.RawMessage(p.ToolCall.Input),
				})
			}
		}
		d, _ := json.Marshal(map[string]any{"role": "assistant", "content": content})
		return []json.RawMessage{d}
	}
	return nil
}

// ThinkingConfig holds the thinking block config and optional top-level output_config.
// output_config must be set at the request body level, not inside thinking.
type ThinkingConfig struct {
	Thinking     map[string]any // goes in body["thinking"]
	OutputConfig map[string]any // goes in body["output_config"] (adaptive only)
	MaxTokens    int
}

func BuildAnthropicThinking(model, level string, maxTokens int) (map[string]any, int) {
	cfg, _ := BuildAnthropicThinkingFull(model, level, maxTokens)
	return cfg.Thinking, cfg.MaxTokens
}

// BuildAnthropicThinkingFull returns full thinking config including output_config.
func BuildAnthropicThinkingFull(model, level string, maxTokens int) (ThinkingConfig, error) {
	if level == "" || level == "disable" {
		return ThinkingConfig{
			Thinking:  map[string]any{"type": "disabled"},
			MaxTokens: maxTokens,
		}, nil
	}
	if isAdaptive(model) {
		cfg := ThinkingConfig{
			Thinking:  map[string]any{"type": "adaptive"},
			MaxTokens: maxTokens,
		}
		if maxTokens < 16000 {
			cfg.MaxTokens = 16000
		}
		// output_config is TOP-LEVEL in the request body, not inside thinking
		if level != "" {
			// Map harness xhigh → Anthropic max (supported: low, medium, high, max)
		effort := level
		if effort == "xhigh" { effort = "max" }
		cfg.OutputConfig = map[string]any{"effort": effort}
		}
		return cfg, nil
	}
	// Legacy budget_tokens for non-adaptive models
	budget := map[string]int{"low": 2048, "medium": 5000, "high": 10240, "xhigh": 32000}
	b, ok := budget[level]
	if !ok {
		b = 10240
	}
	if maxTokens <= b {
		maxTokens = b + 8192
	}
	return ThinkingConfig{
		Thinking:  map[string]any{"type": "enabled", "budget_tokens": b},
		MaxTokens: maxTokens,
	}, nil
}

func IsAdaptiveThinking(model string) bool { return isAdaptive(model) }

func isAdaptive(model string) bool {
	return isAdaptiveRecommended(model) || isAdaptiveOnly(model)
}

func isAdaptiveOnly(model string) bool {
	return containsStr(model, "4-7") || containsStr(model, "opus-5")
}

func isAdaptiveRecommended(model string) bool {
	return containsStr(model, "4-6") || containsStr(model, "4-5") ||
		containsStr(model, "4.5") || containsStr(model, "4.6") || containsStr(model, "4.7")
}

func containsStr(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

func jsonInt(m map[string]any, key string) int { v, _ := m[key].(float64); return int(v) }
func jsonStr(m map[string]any, key string) string { v, _ := m[key].(string); return v }

// BuildAnthropicThinkingFromMeta builds thinking config using authoritative ModelMeta.
// Eliminates version-string heuristics — uses capabilities reported by the API.
func BuildAnthropicThinkingFromMeta(meta *types.ModelMeta, level string, maxTokens int) ThinkingConfig {
	if meta == nil {
		cfg, _ := BuildAnthropicThinkingFull("", level, maxTokens)
		return cfg
	}
	cfg, _ := buildThinkingConfig(meta.ThinkingAdaptive, level, maxTokens)
	return cfg
}

func buildThinkingConfig(adaptive bool, level string, maxTokens int) (ThinkingConfig, error) {
	if level == "" || level == "disable" {
		return ThinkingConfig{Thinking: map[string]any{"type": "disabled"}, MaxTokens: maxTokens}, nil
	}
	if adaptive {
		cfg := ThinkingConfig{Thinking: map[string]any{"type": "adaptive"}, MaxTokens: maxTokens}
		if cfg.MaxTokens < 16000 {
			cfg.MaxTokens = 16000
		}
		if level != "" {
			// Map harness xhigh → Anthropic max (supported: low, medium, high, max)
		effort := level
		if effort == "xhigh" { effort = "max" }
		cfg.OutputConfig = map[string]any{"effort": effort}
		}
		return cfg, nil
	}
	budget := map[string]int{"low": 2048, "medium": 5000, "high": 10240, "xhigh": 32000}
	b, ok := budget[level]
	if !ok {
		b = 10240
	}
	if maxTokens <= b {
		maxTokens = b + 8192
	}
	return ThinkingConfig{Thinking: map[string]any{"type": "enabled", "budget_tokens": b}, MaxTokens: maxTokens}, nil
}
