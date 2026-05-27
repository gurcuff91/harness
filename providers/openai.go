package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
)

// OpenAI implements llm.Provider for the OpenAI API.
type OpenAI struct {
	apiKey       string
	baseURL      string
	client       *http.Client
	cachedModels []llm.ModelMeta
	mu           sync.RWMutex
}

func NewOpenAI() *OpenAI {
	o := &OpenAI{
		apiKey:  config.GetAPIKey("openai"),
		baseURL: "https://api.openai.com/v1",
		client:  &http.Client{},
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func NewOpenAIWithConfig(apiKey, baseURL string) *OpenAI {
	return &OpenAI{apiKey: apiKey, baseURL: baseURL, client: &http.Client{}}
}

func (o *OpenAI) Name() string   { return "openai" }
func (o *OpenAI) IsActive() bool { return o.apiKey != "" }

func (o *OpenAI) Models() []llm.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.cachedModels
}

func (o *OpenAI) FetchModels() []llm.ModelMeta {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "o1", "o3", "o3-mini", "o4-mini"}
	o.cachedModels = nil
	for _, id := range ids {
		if m := llm.LookupModel(id); m != nil {
			o.cachedModels = append(o.cachedModels, *m)
		} else {
			o.cachedModels = append(o.cachedModels, llm.EnrichMeta(llm.ModelMeta{ID: id}))
		}
	}
	return o.cachedModels
}

func (o *OpenAI) CompleteStream(ctx context.Context, req *llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.apiKey, o.baseURL, req, nil, cb)
}

func (o *OpenAI) FormatUserMessage(text string) json.RawMessage {
	return llm.FormatUserMessage(text)
}

func (o *OpenAI) FormatUserMessageWithImages(text string, images []llm.ImageData) json.RawMessage {
	return llm.FormatUserMessageWithImages(text, images)
}

func (o *OpenAI) FormatToolResults(results []llm.ToolResult) []json.RawMessage {
	return llm.FormatToolResults(results)
}
