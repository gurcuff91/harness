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

func DoOpenAIStream(ctx context.Context, client *http.Client, apiKey, baseURL string,
	req *types.Request, extraHeaders map[string]string, cb types.StreamCallback) (*types.Response, error) {

	body, err := BuildOpenAIBody(req)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(data))
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
	return ParseOpenAIStream(httpResp.Body, cb)
}

func BuildOpenAIBody(req *types.Request) (*openAIRequest, error) {
	messages := make([]json.RawMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		sysMsg, _ := json.Marshal(map[string]string{"role": "system", "content": req.SystemPrompt})
		messages = append(messages, sysMsg)
	}
	messages = append(messages, req.Messages...)

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
			body.ReasoningEffort = TranslateThinkingLevel(req.Model, level)
			if isDeepSeek {
				body.Thinking = map[string]any{"type": "enabled"}
			}
		}
	}
	return body, nil
}

func TranslateThinkingLevel(model, level string) string {
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

func ParseOpenAIStream(body io.Reader, cb types.StreamCallback) (*types.Response, error) {
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
			resp.Usage.InputTokens = int(JsonFloat(u, "prompt_tokens"))
			resp.Usage.OutputTokens = int(JsonFloat(u, "completion_tokens"))
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
				idx := int(JsonFloat(tcMap, "index"))
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

	assistantMsg := map[string]any{"role": "assistant", "content": textBuf}
	if len(resp.ToolCalls) > 0 {
		if reasoningBuf != "" {
			assistantMsg["reasoning_content"] = reasoningBuf
		}
		var tcs []map[string]any
		for _, tc := range resp.ToolCalls {
			tcs = append(tcs, map[string]any{
				"id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": string(tc.Input)},
			})
		}
		assistantMsg["tool_calls"] = tcs
	}
	resp.AssistantMessage, _ = json.Marshal(assistantMsg)
	return resp, nil
}

func JsonFloat(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

func FormatUserMessage(text string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"role": "user", "content": text})
	return data
}

func FormatUserMessageWithImages(text string, images []types.ImageData) json.RawMessage {
	var content []map[string]any
	for _, img := range images {
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": "data:" + img.MimeType + ";base64," + img.Base64},
		})
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	data, _ := json.Marshal(map[string]any{"role": "user", "content": content})
	return data
}

func FormatToolResults(results []types.ToolResult) []json.RawMessage {
	var msgs []json.RawMessage
	for _, r := range results {
		data, _ := json.Marshal(map[string]string{
			"role": "tool", "tool_call_id": r.ID, "content": r.Output,
		})
		msgs = append(msgs, data)
	}
	return msgs
}
