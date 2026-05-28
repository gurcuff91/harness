package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// OpenAI implements llm.Provider for the OpenAI API.
type OpenAI struct {
	apiKey  string
	baseURL string
	client  *http.Client
	cache   map[string]types.ModelMeta
	mu      sync.RWMutex
}

func NewOpenAI() *OpenAI {
	o := &OpenAI{
		apiKey:  config.GetAPIKey("openai"),
		baseURL: "https://api.openai.com/v1",
		client:  &http.Client{},
		cache:   make(map[string]types.ModelMeta),
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func NewOpenAIWithConfig(apiKey, baseURL string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{},
		cache:   make(map[string]types.ModelMeta),
	}
}

func (o *OpenAI) Name() string   { return "openai" }
func (o *OpenAI) IsActive() bool { return o.apiKey != "" }

func (o *OpenAI) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *OpenAI) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (o *OpenAI) FetchModels() []types.ModelMeta {
	ids := []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "o1", "o3", "o3-mini", "o4-mini"}
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta, len(ids))
	for _, id := range ids {
		if m := llm.LookupModel(id); m != nil {
			o.cache[id] = *m
		} else {
			o.cache[id] = llm.EnrichMeta(types.ModelMeta{ID: id})
		}
	}
	o.mu.Unlock()
	return o.Models()
}

func (o *OpenAI) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.apiKey, o.baseURL, req, nil, cb)
}

func (o *OpenAI) FormatUserMessage(text string) json.RawMessage {
	return llm.FormatUserMessage(text)
}

func (o *OpenAI) FormatUserMessageWithImages(text string, images []types.ImageData) json.RawMessage {
	return llm.FormatUserMessageWithImages(text, images)
}

func (o *OpenAI) FormatToolResults(results []types.ToolResult) []json.RawMessage {
	return llm.FormatToolResults(results)
}
