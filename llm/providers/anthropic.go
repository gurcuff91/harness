package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gurcuff91/harness/llm"
)

const anthropicAPI = "https://api.anthropic.com/v1/messages"

// Anthropic implements llm.Provider for Claude models.
type Anthropic struct {
	apiKey string
	model  string
	client *http.Client
}

func NewAnthropic(apiKey, model string) *Anthropic {
	return &Anthropic{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}
}

func (a *Anthropic) Model() string { return a.model }

// anthropicRequest is the Anthropic Messages API request body.
type anthropicRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    string            `json:"system,omitempty"`
	Messages  []json.RawMessage `json:"messages"`
	Tools     []anthropicTool   `json:"tools,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicResponse is the Anthropic Messages API response body.
type anthropicResponse struct {
	ID      string             `json:"id"`
	Content []anthropicContent `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	StopReason string `json:"stop_reason"`
}

type anthropicContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Thinking string          `json:"thinking,omitempty"`
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// Complete sends a non-streaming request (fallback).
func (a *Anthropic) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return a.CompleteStream(ctx, req, nil)
}

// CompleteStream sends a streaming request and emits events via callback.
func (a *Anthropic) CompleteStream(ctx context.Context, req *llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	// Convert tools to Anthropic format
	var aTools []anthropicTool
	for _, t := range req.Tools {
		aTools = append(aTools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	thinkingCfg, adjustedMaxTokens := buildAnthropicThinking(a.model, req.MaxTokens)

	body := map[string]any{
		"model":      a.model,
		"max_tokens": adjustedMaxTokens,
		"system":     req.SystemPrompt,
		"messages":   req.Messages,
		"tools":      aTools,
		"stream":     true,
		"thinking":   thinkingCfg,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPI, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Reuse the same SSE stream parser as ClaudeOAuth
	return parseAnthropicStream(httpResp.Body, cb, func(name string) string { return name })
}

// parseAnthropicStream processes Anthropic SSE events. Shared between Anthropic and ClaudeOAuth.
// unmapTool converts tool names from wire format back to harness format.
func parseAnthropicStream(body io.Reader, cb llm.StreamCallback, unmapTool func(string) string) (*llm.Response, error) {
	emit := func(e llm.StreamEvent) {
		if cb != nil {
			cb(e)
		}
	}

	resp := &llm.Response{}
	var thinkingBlocks []map[string]any // preserve per-block signatures

	type blockState struct {
		blockType string
		text      string
		thinking  string
		signature string // thinking block cryptographic signature
		toolID    string
		toolName  string
		toolJSON  string
	}
	blocks := make(map[int]*blockState)

	for sse := range llm.ParseSSE(body) {
		if sse.Event == "error" {
			emit(llm.StreamEvent{Type: llm.StreamError, Delta: sse.Data})
			return nil, fmt.Errorf("stream error: %s", sse.Data)
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(sse.Data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
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

			if bt == "tool_use" {
				bs.toolID, _ = cb2["id"].(string)
				bs.toolName = unmapTool(jsonStr(cb2, "name"))
				emit(llm.StreamEvent{Type: llm.StreamToolStart, ToolID: bs.toolID, ToolName: bs.toolName})
			}

		case "content_block_delta":
			idx := jsonInt(event, "index")
			bs := blocks[idx]
			if bs == nil {
				continue
			}
			delta, _ := event["delta"].(map[string]any)
			dt, _ := delta["type"].(string)

			switch dt {
			case "text_delta":
				text, _ := delta["text"].(string)
				bs.text += text
				emit(llm.StreamEvent{Type: llm.StreamTextDelta, Delta: text})
			case "thinking_delta":
				thinking, _ := delta["thinking"].(string)
				bs.thinking += thinking
				emit(llm.StreamEvent{Type: llm.StreamThinkingDelta, Delta: thinking})
			case "signature_delta":
				sig, _ := delta["signature"].(string)
				bs.signature += sig
			case "input_json_delta":
				partial, _ := delta["partial_json"].(string)
				bs.toolJSON += partial
				emit(llm.StreamEvent{Type: llm.StreamToolDelta, Delta: partial})
			}

		case "content_block_stop":
			idx := jsonInt(event, "index")
			bs := blocks[idx]
			if bs == nil {
				continue
			}
			switch bs.blockType {
			case "text":
				if resp.Text != "" {
					resp.Text += "\n"
				}
				resp.Text += bs.text
			case "thinking":
				if resp.Thinking != "" {
					resp.Thinking += "\n"
				}
				resp.Thinking += bs.thinking
				// Preserve thinking block with signature for history replay
				tb := map[string]any{
					"type":     "thinking",
					"thinking": bs.thinking,
				}
				if bs.signature != "" {
					tb["signature"] = bs.signature
				}
				thinkingBlocks = append(thinkingBlocks, tb)
			case "tool_use":
				inputJSON := json.RawMessage(bs.toolJSON)
				if len(inputJSON) == 0 {
					inputJSON = json.RawMessage("{}")
				}
				resp.ToolCalls = append(resp.ToolCalls, llm.ToolCall{
					ID: bs.toolID, Name: bs.toolName, Input: inputJSON,
				})
				emit(llm.StreamEvent{Type: llm.StreamToolEnd, ToolID: bs.toolID, ToolName: bs.toolName, ToolArgs: json.RawMessage(bs.toolJSON)})
			}
			delete(blocks, idx)

		case "message_delta":
			if u, ok := event["usage"].(map[string]any); ok {
				if out := jsonInt(u, "output_tokens"); out > 0 {
					resp.Usage.OutputTokens = out
				}
				if cr := jsonInt(u, "cache_read_input_tokens"); cr > 0 {
					resp.Usage.CacheRead = cr
				}
				if cw := jsonInt(u, "cache_creation_input_tokens"); cw > 0 {
					resp.Usage.CacheWrite = cw
				}
			}

		case "message_stop":
			emit(llm.StreamEvent{
				Type:         llm.StreamUsage,
				InputTokens:  resp.Usage.InputTokens,
				OutputTokens: resp.Usage.OutputTokens,
				CacheRead:    resp.Usage.CacheRead,
				CacheWrite:   resp.Usage.CacheWrite,
			})
			emit(llm.StreamEvent{Type: llm.StreamDone})
		}
	}

	// Build AssistantMessage for history
	var contentBlocks []map[string]any
	for _, tb := range thinkingBlocks {
		contentBlocks = append(contentBlocks, tb)
	}
	if resp.Text != "" {
		contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": resp.Text})
	}
	for _, tc := range resp.ToolCalls {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": json.RawMessage(tc.Input),
		})
	}
	assistantMsg := map[string]any{"role": "assistant", "content": contentBlocks}
	resp.AssistantMessage, _ = json.Marshal(assistantMsg)

	return resp, nil
}

func jsonInt(m map[string]any, key string) int {
	v, _ := m[key].(float64)
	return int(v)
}

func jsonStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func (a *Anthropic) FormatUserMessage(text string) json.RawMessage {
	msg := map[string]any{
		"role":    "user",
		"content": text,
	}
	data, _ := json.Marshal(msg)
	return data
}

func (a *Anthropic) FormatUserMessageWithImages(text string, images []llm.ImageData) json.RawMessage {
	return formatAnthropicUserWithImages(text, images)
}

// formatAnthropicUserWithImages builds an Anthropic user message with text + image blocks.
func formatAnthropicUserWithImages(text string, images []llm.ImageData) json.RawMessage {
	var content []map[string]any

	// Images first (Anthropic recommends images before text)
	for _, img := range images {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]string{
				"type":       "base64",
				"media_type": img.MimeType,
				"data":       img.Base64,
			},
		})
	}

	// Text after images
	if text != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": text,
		})
	}

	msg := map[string]any{
		"role":    "user",
		"content": content,
	}
	data, _ := json.Marshal(msg)
	return data
}

func (a *Anthropic) FormatToolResults(results []llm.ToolResult) []json.RawMessage {
	// Anthropic wraps all tool results in a single user message
	var content []map[string]any
	for _, r := range results {
		block := map[string]any{
			"type":       "tool_result",
			"tool_use_id": r.ID,
			"content":    r.Output,
		}
		if r.IsErr {
			block["is_error"] = true
		}
		content = append(content, block)
	}
	msg := map[string]any{
		"role":    "user",
		"content": content,
	}
	data, _ := json.Marshal(msg)
	return []json.RawMessage{data}
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}

// buildAnthropicThinking returns the thinking config and adjusted max_tokens
// for Anthropic models. Uses adaptive thinking for newer models.
//
// Mapping from universal levels (settings.json) to Anthropic effort:
//   low → low, medium → medium, high → high, xhigh → xhigh
//
// Model detection:
//   claude-*-4-7 and newer → adaptive (budget_tokens removed)
//   claude-*-4-6, claude-*-4-5 → adaptive recommended (budget_tokens deprecated)
//   older models → budget_tokens (legacy)
func buildAnthropicThinking(model string, maxTokens int) (map[string]any, int) {
	level := GetThinking()

	// disabled — no thinking
	if level == "disable" {
		return map[string]any{"type": "disabled"}, maxTokens
	}

	// Opus 4.7+ only supports adaptive
	useAdaptive := isAdaptiveOnlyModel(model)
	// 4.6/4.5 support both but adaptive is recommended
	if !useAdaptive && isAdaptiveRecommendedModel(model) {
		useAdaptive = true
	}

	if useAdaptive {
		cfg := map[string]any{
			"type": "adaptive",
		}
		// output_config.effort controls depth
		if level != "" {
			cfg["output_config"] = map[string]any{"effort": level}
		}
		// Adaptive thinking needs generous max_tokens
		if maxTokens < 16000 {
			maxTokens = 16000
		}
		return cfg, maxTokens
	}

	// Legacy: budget_tokens
	budget := budgetForLevel(level)
	if maxTokens <= budget {
		maxTokens = budget + 8192
	}
	return map[string]any{
		"type":          "enabled",
		"budget_tokens": budget,
	}, maxTokens
}

func isAdaptiveOnlyModel(model string) bool {
	// Opus 4.7+ removed budget_tokens support
	return strings.Contains(model, "4-7") || strings.Contains(model, "opus-4-7") ||
		strings.Contains(model, "opus-5")
}

func isAdaptiveRecommendedModel(model string) bool {
	return strings.Contains(model, "4-6") || strings.Contains(model, "sonnet-4-6") ||
		strings.Contains(model, "4-5") || strings.Contains(model, "4.5") ||
		strings.Contains(model, "4.6") || strings.Contains(model, "4.7")
}

func budgetForLevel(level string) int {
	switch level {
	case "low":
		return 2048
	case "medium":
		return 5000
	case "xhigh":
		return 32000
	default: // high
		return 10240
	}
}
