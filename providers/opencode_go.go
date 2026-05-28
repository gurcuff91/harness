package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

const openCodeGoURL = "https://opencode.ai/zen/go/v1"

type OpenCodeGo struct {
	apiKey string
	client *http.Client
	cache  map[string]types.ModelMeta
	mu     sync.RWMutex
}

func NewOpenCodeGo() *OpenCodeGo {
	o := &OpenCodeGo{
		apiKey: config.GetAPIKey("opencode-go"),
		client: &http.Client{},
		cache:  make(map[string]types.ModelMeta),
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func (o *OpenCodeGo) Name() string   { return "opencode-go" }
func (o *OpenCodeGo) IsActive() bool { return o.apiKey != "" }

func (o *OpenCodeGo) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *OpenCodeGo) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (o *OpenCodeGo) FetchModels() []types.ModelMeta {
	metas := fetchOpenCodeGoModels(o.apiKey)
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		o.cache[m.ID] = m
	}
	o.mu.Unlock()
	return metas
}

func (o *OpenCodeGo) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.apiKey, openCodeGoURL, req, nil, cb)
}

func (o *OpenCodeGo) FormatUserMessage(text string) json.RawMessage {
	return llm.FormatUserMessage(text)
}

func (o *OpenCodeGo) FormatUserMessageWithImages(text string, images []types.ImageData) json.RawMessage {
	return llm.FormatUserMessageWithImages(text, images)
}

func (o *OpenCodeGo) FormatToolResults(results []types.ToolResult) []json.RawMessage {
	return llm.FormatToolResults(results)
}

func fetchOpenCodeGoModels(apiKey string) []types.ModelMeta {
	req, _ := http.NewRequest("GET", openCodeGoURL+"/models", nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	metas := make([]types.ModelMeta, 0, len(result.Data))
	for _, m := range result.Data {
		meta := types.ModelMeta{
			ID:            m.ID,
			ContextWindow: llm.InferContextWindow(m.ID),
			MaxTokens:     32000,
			Vision:        llm.InferVision(m.ID),
		}
		metas = append(metas, llm.EnrichMeta(meta))
	}
	return metas
}
