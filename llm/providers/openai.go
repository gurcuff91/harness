package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/llm"
)

// OpenAI implements llm.Provider for GPT models and OpenAI-compatible APIs (Ollama, etc).
type OpenAI struct {
	apiKey        string
	baseURL       string
	model         string
	client        *http.Client
	thinking      bool   // request thinking/reasoning from model
	thinkingLevel string // universal level: low/medium/high/xhigh
	subscription  bool   // true = flat sub or local compute, not pay-per-token
}

func NewOpenAI(apiKey, baseURL, model string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{},
	}
}

// WithThinking enables reasoning/thinking for supported models.
func (o *OpenAI) WithThinking(enabled bool) *OpenAI {
	o.thinking = enabled
	return o
}

// WithThinkingLevel sets the thinking effort level.
func (o *OpenAI) WithThinkingLevel(level string) *OpenAI {
	o.thinkingLevel = level
	return o
}

// translateThinkingLevel maps universal levels (low/medium/high/xhigh)
// to provider-specific reasoning_effort values.
func translateThinkingLevel(model, level string) string {
	if level == "" {
		level = "high" // default
	}
	// DeepSeek: only high/max
	if strings.Contains(model, "deepseek") {
		switch level {
		case "xhigh":
			return "max"
		default:
			return "high"
		}
	}
	// OpenAI o-series: low/medium/high (xhigh → high, no max)
	if strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4") || strings.HasPrefix(model, "o1-") {
		switch level {
		case "low":
			return "low"
		case "medium":
			return "medium"
		default: // high, xhigh
			return "high"
		}
	}
	// Other models (GLM, Kimi, etc): don't send reasoning_effort, they use think:true only
	return ""
}

func (o *OpenAI) Model() string              { return o.model }
func (o *OpenAI) IsSubscription() bool       { return o.subscription }
func (o *OpenAI) SetThinkingLevel(level string) {
	o.thinkingLevel = level
}

type openaiRequest struct {
	Model           string            `json:"model"`
	Messages        []json.RawMessage `json:"messages"`
	Tools           []openaiTool      `json:"tools,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Stream          bool              `json:"stream"`
	StreamOptions   *streamOptions    `json:"stream_options,omitempty"`
	Think           *bool             `json:"think,omitempty"`            // Ollama
	ReasoningEffort string            `json:"reasoning_effort,omitempty"` // DeepSeek/OpenAI
	Thinking        map[string]any    `json:"thinking,omitempty"`         // DeepSeek extra
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiTool struct {
	Type     string         `json:"type"`
	Function openaiFunction `json:"function"`
}

type openaiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openaiResponse struct {
	Choices []openaiChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type openaiChoice struct {
	Message openaiMessage `json:"message"`
}

type openaiMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (o *OpenAI) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return o.CompleteStream(ctx, req, nil)
}

func (o *OpenAI) CompleteStream(ctx context.Context, req *llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	// Prepend system message
	messages := make([]json.RawMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		sysMsg, _ := json.Marshal(map[string]string{
			"role":    "system",
			"content": req.SystemPrompt,
		})
		messages = append(messages, sysMsg)
	}
	messages = append(messages, req.Messages...)

	// Convert tools
	var oTools []openaiTool
	for _, t := range req.Tools {
		oTools = append(oTools, openaiTool{
			Type: "function",
			Function: openaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	body := openaiRequest{
		Model:         o.model,
		Messages:      messages,
		Tools:         oTools,
		MaxTokens:     req.MaxTokens,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	if o.thinking {
		level := config.GetThinking()
		isDeepSeek := strings.Contains(o.model, "deepseek")

		if level == "disable" {
			// Ollama/OpenCode: think=false
			t := false
			body.Think = &t
			// DeepSeek: also send thinking.type=disabled
			if isDeepSeek {
				body.Thinking = map[string]any{"type": "disabled"}
			}
		} else {
			t := true
			body.Think = &t
			effort := translateThinkingLevel(o.model, level)
			body.ReasoningEffort = effort
			// DeepSeek: also send thinking.type=enabled
			if isDeepSeek {
				body.Thinking = map[string]any{"type": "enabled"}
			}
		}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := o.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	httpResp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("openai API error %d: %s", httpResp.StatusCode, string(body))
	}

	return o.parseStream(httpResp.Body, cb)
}

func (o *OpenAI) parseStream(body io.Reader, cb llm.StreamCallback) (*llm.Response, error) {
	emit := func(e llm.StreamEvent) {
		if cb != nil {
			cb(e)
		}
	}

	resp := &llm.Response{}

	// Per-tool-call state
	type toolState struct {
		id       string
		name     string
		argsBuf  string
	}
	toolsByIdx := map[int]*toolState{}

	var textBuf string
	var reasoningBuf string

	for sse := range llm.ParseSSE(body) {
		if sse.Data == "[DONE]" {
			break
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(sse.Data), &event); err != nil {
			continue
		}

		choices, _ := event["choices"].([]any)

		// Usage can arrive in a chunk with empty choices
		if u, ok := event["usage"].(map[string]any); ok {
			resp.Usage.InputTokens = int(jsonFloat(u, "prompt_tokens"))
			resp.Usage.OutputTokens = int(jsonFloat(u, "completion_tokens"))
		}

		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)

		// Reasoning/thinking delta — DeepSeek uses "reasoning_content", Ollama uses "reasoning"
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			reasoningBuf += reasoning
			emit(llm.StreamEvent{Type: llm.StreamThinkingDelta, Delta: reasoning})
		} else if reasoning, ok := delta["reasoning"].(string); ok && reasoning != "" {
			reasoningBuf += reasoning
			emit(llm.StreamEvent{Type: llm.StreamThinkingDelta, Delta: reasoning})
		}

		// Text delta
		if text, ok := delta["content"].(string); ok && text != "" {
			textBuf += text
			emit(llm.StreamEvent{Type: llm.StreamTextDelta, Delta: text})
		}

		// Tool calls
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
					emit(llm.StreamEvent{Type: llm.StreamToolStart, ToolID: ts.id, ToolName: ts.name})
				}
				ts := toolsByIdx[idx]
				if fn, ok := tcMap["function"].(map[string]any); ok {
					if args, ok := fn["arguments"].(string); ok {
						ts.argsBuf += args
						emit(llm.StreamEvent{Type: llm.StreamToolDelta, Delta: args})
					}
				}
			}
		}
	}

	// Finalize
	resp.Text = textBuf
	if textBuf != "" {
		emit(llm.StreamEvent{Type: llm.StreamTextDelta, Delta: ""}) // signal end
	}

	for _, ts := range toolsByIdx {
		input := json.RawMessage(ts.argsBuf)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		resp.ToolCalls = append(resp.ToolCalls, llm.ToolCall{
			ID: ts.id, Name: ts.name, Input: input,
		})
		emit(llm.StreamEvent{Type: llm.StreamToolEnd, ToolID: ts.id, ToolName: ts.name, ToolArgs: json.RawMessage(ts.argsBuf)})
	}

	// Emit usage and done
	emit(llm.StreamEvent{
		Type:         llm.StreamUsage,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	})
	emit(llm.StreamEvent{Type: llm.StreamDone})

	// Build AssistantMessage
	var contentBlocks []map[string]any
	if textBuf != "" {
		contentBlocks = append(contentBlocks, map[string]any{"role": "assistant", "content": textBuf})
	}
	for _, tc := range resp.ToolCalls {
		contentBlocks = append(contentBlocks, map[string]any{
			"role": "assistant",
			"tool_calls": []map[string]any{{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": string(tc.Input)}}},
		})
	}
	if len(contentBlocks) > 0 {
		assistantMsg := map[string]any{"role": "assistant", "content": textBuf}
		if len(resp.ToolCalls) > 0 {
			// DeepSeek: reasoning_content MUST be passed back when there are tool calls
			if reasoningBuf != "" {
				assistantMsg["reasoning_content"] = reasoningBuf
			}
			var tcs []map[string]any
			for _, tc := range resp.ToolCalls {
				tcs = append(tcs, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": string(tc.Input)}})
			}
			assistantMsg["tool_calls"] = tcs
		}
		resp.AssistantMessage, _ = json.Marshal(assistantMsg)
	}

	return resp, nil
}

func jsonFloat(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

func (o *OpenAI) FormatUserMessage(text string) json.RawMessage {
	msg := map[string]string{
		"role":    "user",
		"content": text,
	}
	data, _ := json.Marshal(msg)
	return data
}

func (o *OpenAI) FormatUserMessageWithImages(text string, images []llm.ImageData) json.RawMessage {
	var content []map[string]any
	for _, img := range images {
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": "data:" + img.MimeType + ";base64," + img.Base64,
			},
		})
	}
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

func (o *OpenAI) FormatToolResults(results []llm.ToolResult) []json.RawMessage {
	// OpenAI expects separate messages per tool result with role "tool"
	var msgs []json.RawMessage
	for _, r := range results {
		msg := map[string]string{
			"role":         "tool",
			"tool_call_id": r.ID,
			"content":      r.Output,
		}
		data, _ := json.Marshal(msg)
		msgs = append(msgs, data)
	}
	return msgs
}
