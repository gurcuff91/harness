package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
)

const openCodeGoURL = "https://opencode.ai/zen/go/v1"

type OpenCodeGo struct {
	*OpenAI
	apiKey       string
	cachedModels []llm.ModelMeta
}

func NewOpenCodeGo() *OpenCodeGo {
	apiKey := config.GetAPIKey("opencode-go")
	o := &OpenCodeGo{
		OpenAI: NewOpenAIWithConfig(apiKey, openCodeGoURL),
		apiKey: apiKey,
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func (o *OpenCodeGo) Name() string    { return "opencode-go" }
func (o *OpenCodeGo) IsActive() bool  { return o.apiKey != "" }

func (o *OpenCodeGo) Models() []llm.ModelMeta { return o.cachedModels }

func (o *OpenCodeGo) FetchModels() []llm.ModelMeta {
	o.cachedModels = fetchOpenCodeGoModels(o.apiKey)
	return o.cachedModels
}

func (o *OpenCodeGo) CompleteStream(ctx context.Context, req *llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	return llm.DoOpenAIStream(ctx, o.OpenAI.client, o.OpenAI.apiKey, o.OpenAI.baseURL, req, nil, cb)
}

func fetchOpenCodeGoModels(apiKey string) []llm.ModelMeta {
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", openCodeGoURL+"/models", nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
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

	var metas []llm.ModelMeta
	for _, m := range result.Data {
		meta := llm.ModelMeta{
			ID:            m.ID,
			ContextWindow: llm.InferContextWindow(m.ID),
			MaxTokens:     32000,
			Vision:        llm.InferVision(m.ID),
		}
		metas = append(metas, llm.EnrichMeta(meta))
	}
	return metas
}
