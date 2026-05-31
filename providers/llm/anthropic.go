package llm

import (
	"github.com/gurcuff91/harness/types"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func DoAnthropicStream(ctx context.Context, client *http.Client, apiURL, apiKey string,
	req *types.Request, extraHeaders map[string]string, unmapTool func(string) string,
	cb types.StreamCallback) (*types.Response, error) {

	var aTools []AnthropicTool
	for _, t := range req.Tools {
		aTools = append(aTools, AnthropicTool{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		})
	}

	thinkingCfg, maxTokens := BuildAnthropicThinking(req.Model, req.ThinkingLevel, req.MaxTokens)

	wireMsgs := make([]json.RawMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		for _, w := range TranslateMessageToAnthropic(m) {
			wireMsgs = append(wireMsgs, w)
		}
	}

	body := map[string]any{
		"model": req.Model, "max_tokens": maxTokens,
		"system": req.SystemPrompt, "messages": wireMsgs,
		"tools": aTools, "stream": true, "thinking": thinkingCfg,
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
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
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
		return nil, fmt.Errorf("anthropic API error %d: %s", httpResp.StatusCode, string(b))
	}

	if unmapTool == nil {
		unmapTool = func(s string) string { return s }
	}
	return ParseAnthropicStream(httpResp.Body, cb, unmapTool)
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
			if bt == "tool_use" {
				bs.toolID, _ = cb2["id"].(string)
				bs.toolName = unmapTool(jsonStr(cb2, "name"))
				emit(types.StreamEvent{Type: types.StreamToolStart, ToolID: bs.toolID, ToolName: bs.toolName})
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

func BuildAnthropicThinking(model, level string, maxTokens int) (map[string]any, int) {
	if level == "" || level == "disable" {
		return map[string]any{"type": "disabled"}, maxTokens
	}
	useAdaptive := isAdaptive(model)
	if useAdaptive {
		cfg := map[string]any{"type": "adaptive"}
		if level != "" { cfg["output_config"] = map[string]any{"effort": level} }
		if maxTokens < 16000 { maxTokens = 16000 }
		return cfg, maxTokens
	}
	budget := map[string]int{"low": 2048, "medium": 5000, "xhigh": 32000}
	b, ok := budget[level]
	if !ok { b = 10240 }
	if maxTokens <= b { maxTokens = b + 8192 }
	return map[string]any{"type": "enabled", "budget_tokens": b}, maxTokens
}

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
