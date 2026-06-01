package llm

import (
	"github.com/gurcuff91/harness/types"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIRequest wraps types.Request for OpenAI-compatible providers.
// Embed and extend when provider-specific overrides are needed.
// Today it's a thin wrapper — future providers can add fields without
// changing the DoOpenAIStream signature.
type OpenAIRequest struct {
	*types.Request
}

// openAIWireRequest is the internal wire format sent to the API.
type openAIRequest struct {
	Model           string          `json:"model"`
	Messages        []json.RawMessage `json:"messages"`
	Tools           []openAITool    `json:"tools,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Stream          bool            `json:"stream"`
	StreamOptions   *streamOpts     `json:"stream_options,omitempty"`
	Think           *bool           `json:"think,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Thinking        map[string]any  `json:"thinking,omitempty"`
}

type streamOpts struct{ IncludeUsage bool `json:"include_usage"` }
type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}
type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func DoOpenAIStream(ctx context.Context, client *http.Client, apiURL, apiKey string,
	req *OpenAIRequest, extraHeaders map[string]string, cb types.StreamCallback) (*types.Response, error) {

	body, err := buildOpenAIBody(req.Request)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range extraHeaders {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("openai API error %d: %s", httpResp.StatusCode, string(b))
	}
	return parseOpenAIStream(httpResp.Body, cb)
}

// translateMessageToOpenAI converts a types.Message to OpenAI wire format.
func translateMessageToOpenAI(msg types.Message) []json.RawMessage {
	switch msg.Role {
	case types.RoleUser:
		// Check if it's a tool result message
		var toolResults []map[string]any
		var contentParts []map[string]any
		for _, p := range msg.Parts {
			if p.ToolResult != nil {
				toolResults = append(toolResults, map[string]any{
					"role": "tool", "tool_call_id": p.ToolResult.ID, "content": p.ToolResult.Output,
				})
			} else if p.Image != nil {
				contentParts = append(contentParts, map[string]any{
					"type": "image_url",
					"image_url": map[string]string{"url": "data:" + p.Image.MimeType + ";base64," + p.Image.Base64},
				})
			} else if p.Text != "" {
				contentParts = append(contentParts, map[string]any{"type": "text", "text": p.Text})
			}
		}
		// Tool results become individual tool messages
		if len(toolResults) > 0 {
			var msgs []json.RawMessage
			for _, tr := range toolResults {
				d, _ := json.Marshal(tr)
				msgs = append(msgs, d)
			}
			return msgs
		}
		// User text/image message
		if len(contentParts) == 1 && contentParts[0]["type"] == "text" {
			d, _ := json.Marshal(map[string]string{"role": "user", "content": contentParts[0]["text"].(string)})
			return []json.RawMessage{d}
		}
		d, _ := json.Marshal(map[string]any{"role": "user", "content": contentParts})
		return []json.RawMessage{d}

	case types.RoleAssistant:
		wire := map[string]any{"role": "assistant", "content": ""}
		var tcs []map[string]any
		for _, p := range msg.Parts {
			if p.Text != "" {
				wire["content"] = p.Text
			} else if p.Thinking != nil {
				wire["reasoning_content"] = p.Thinking.Content
			} else if p.ToolCall != nil {
				tcs = append(tcs, map[string]any{
					"id": p.ToolCall.ID, "type": "function",
					"function": map[string]any{"name": p.ToolCall.Name, "arguments": string(p.ToolCall.Input)},
				})
			}
		}
		if len(tcs) > 0 {
			wire["tool_calls"] = tcs
		}
		d, _ := json.Marshal(wire)
		return []json.RawMessage{d}
	}
	return nil
}

func buildOpenAIBody(req *types.Request) (*openAIRequest, error) {
	messages := make([]json.RawMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		sysMsg, _ := json.Marshal(map[string]string{"role": "system", "content": req.SystemPrompt})
		messages = append(messages, sysMsg)
	}
	for _, m := range req.Messages {
		messages = append(messages, translateMessageToOpenAI(m)...)
	}

	var tools []openAITool
	for _, t := range req.Tools {
		tools = append(tools, openAITool{
			Type: "function",
			Function: openAIFunction{Name: t.Name, Description: t.Description, Parameters: t.InputSchema},
		})
	}

	body := &openAIRequest{
		Model: req.Model, Messages: messages, Tools: tools,
		MaxTokens: req.MaxTokens, Stream: true,
		StreamOptions: &streamOpts{IncludeUsage: true},
	}

	if req.ThinkingLevel != "" {
		level := req.ThinkingLevel
		isDeepSeek := strings.Contains(req.Model, "deepseek")
		if level == "disable" {
			t := false
			body.Think = &t
			if isDeepSeek {
				body.Thinking = map[string]any{"type": "disabled"}
			}
		} else {
			t := true
			body.Think = &t
			body.ReasoningEffort = translateThinkingLevel(req.Model, level)
			if isDeepSeek {
				body.Thinking = map[string]any{"type": "enabled"}
			}
		}
	}
	return body, nil
}

func translateThinkingLevel(model, level string) string {
	if strings.Contains(model, "deepseek") {
		if level == "xhigh" {
			return "max"
		}
		return "high"
	}
	if strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") {
		switch level {
		case "low":
			return "low"
		case "medium":
			return "medium"
		default:
			return "high"
		}
	}
	return ""
}

func parseOpenAIStream(body io.Reader, cb types.StreamCallback) (*types.Response, error) {
	emit := func(e types.StreamEvent) {
		if cb != nil {
			cb(e)
		}
	}

	resp := &types.Response{}
	type toolState struct {
		id      string
		name    string
		argsBuf string
	}
	toolsByIdx := map[int]*toolState{}
	var textBuf, reasoningBuf string

	for sse := range ParseSSE(body) {
		if sse.Data == "[DONE]" {
			break
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(sse.Data), &event); err != nil {
			continue
		}
		if u, ok := event["usage"].(map[string]any); ok {
			resp.Usage.InputTokens = int(jsonFloat(u, "prompt_tokens"))
			resp.Usage.OutputTokens = int(jsonFloat(u, "completion_tokens"))
		}
		choices, _ := event["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)

		if r, ok := delta["reasoning_content"].(string); ok && r != "" {
			reasoningBuf += r
			emit(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: r})
		} else if r, ok := delta["reasoning"].(string); ok && r != "" {
			reasoningBuf += r
			emit(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: r})
		}
		if text, ok := delta["content"].(string); ok && text != "" {
			textBuf += text
			emit(types.StreamEvent{Type: types.StreamTextDelta, Delta: text})
		}
		if tcs, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				tcMap, _ := tc.(map[string]any)
				idx := int(jsonFloat(tcMap, "index"))
				if _, exists := toolsByIdx[idx]; !exists {
					ts := &toolState{}
					if fn, ok := tcMap["function"].(map[string]any); ok {
						ts.name, _ = fn["name"].(string)
					}
					ts.id, _ = tcMap["id"].(string)
					toolsByIdx[idx] = ts
					emit(types.StreamEvent{Type: types.StreamToolStart, ToolID: ts.id, ToolName: ts.name})
				}
				ts := toolsByIdx[idx]
				if fn, ok := tcMap["function"].(map[string]any); ok {
					if args, ok := fn["arguments"].(string); ok {
						ts.argsBuf += args
						emit(types.StreamEvent{Type: types.StreamToolDelta, Delta: args})
					}
				}
			}
		}
	}

	resp.Text = textBuf
	for _, ts := range toolsByIdx {
		input := json.RawMessage(ts.argsBuf)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		resp.ToolCalls = append(resp.ToolCalls, types.ToolCall{
			ID: ts.id, Name: ts.name, Input: input,
		})
		emit(types.StreamEvent{Type: types.StreamToolEnd, ToolID: ts.id, ToolName: ts.name, ToolArgs: input})
	}
	emit(types.StreamEvent{
		Type: types.StreamUsage, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
	})
	emit(types.StreamEvent{Type: types.StreamDone})

	resp.Message = types.NewAssistantToolCallMessage(textBuf, reasoningBuf, "", resp.ToolCalls)
	return resp, nil
}

// jsonFloat is an internal helper for parsing float64 from SSE event maps.
func jsonFloat(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}
