package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/types"
)

// Ollama wraps OpenAI-compatible streaming for local Ollama instances.
type Ollama struct {
	baseURL string
	client  *http.Client
	cache   map[string]types.ModelMeta
	mu      sync.RWMutex
}

func NewOllama() *Ollama {
	o := &Ollama{
		baseURL: config.GetOllamaURL(),
		client:  &http.Client{},
		cache:   make(map[string]types.ModelMeta),
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func (o *Ollama) Name() string   { return "ollama" }
func (o *Ollama) IsActive() bool { return OllamaAvailable() }

func (o *Ollama) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *Ollama) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (o *Ollama) FetchModels() []types.ModelMeta {
	metas := fetchOllamaModels(o.baseURL)
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		o.cache[m.ID] = m
	}
	o.mu.Unlock()
	return metas
}

func (o *Ollama) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, "", o.baseURL+"/v1", req, nil, cb)
}

func (o *Ollama) FormatUserMessage(text string) json.RawMessage {
	return llm.FormatUserMessage(text)
}

func (o *Ollama) FormatUserMessageWithImages(text string, images []types.ImageData) json.RawMessage {
	return llm.FormatUserMessageWithImages(text, images)
}

func (o *Ollama) FormatToolResults(results []types.ToolResult) []json.RawMessage {
	return llm.FormatToolResults(results)
}

func OllamaAvailable() bool {
	url := config.GetOllamaURL()
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url + "/api/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func fetchOllamaModels(baseURL string) []types.ModelMeta {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(baseURL + "/api/tags")
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name    string `json:"name"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var metas []types.ModelMeta
	for _, m := range result.Models {
		meta := types.ModelMeta{
			ID:          m.Name,
			DisplayName: fmt.Sprintf("%s (%s)", m.Name, m.Details.ParameterSize),
		}
		llm.ApplyRegistryPricing(&meta)
		metas = append(metas, meta)
	}
	return metas
}
