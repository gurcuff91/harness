package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
)

const ollamaCloudURL = "https://ollama.com/v1"

type OllamaCloud struct {
	*OpenAI
	apiKey       string
	cachedModels []llm.ModelMeta
}

func NewOllamaCloud() *OllamaCloud {
	apiKey := config.GetAPIKey("ollama-cloud")
	o := &OllamaCloud{
		OpenAI: NewOpenAIWithConfig(apiKey, ollamaCloudURL),
		apiKey: apiKey,
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func (o *OllamaCloud) Name() string    { return "ollama-cloud" }
func (o *OllamaCloud) IsActive() bool  { return o.apiKey != "" }

func (o *OllamaCloud) Models() []llm.ModelMeta { return o.cachedModels }

func (o *OllamaCloud) FetchModels() []llm.ModelMeta {
	req, _ := http.NewRequest("GET", "https://ollama.com/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil
	}

	o.cachedModels = nil
	for _, item := range list.Data {
		meta := llm.ModelMeta{
			ID:            item.ID,
			ContextWindow: llm.InferContextWindow(item.ID),
			MaxTokens:     32000,
			Vision:        llm.InferVision(item.ID),
		}
		if info := fetchOllamaModelInfo(item.ID); info != nil {
			meta = *info
		}
		o.cachedModels = append(o.cachedModels, meta)
	}
	return o.cachedModels
}

func (o *OllamaCloud) CompleteStream(ctx context.Context, req *llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	return llm.DoOpenAIStream(ctx, o.OpenAI.client, o.OpenAI.apiKey, o.OpenAI.baseURL, req, nil, cb)
}

func fetchOllamaModelInfo(name string) *llm.ModelMeta {
	body, _ := json.Marshal(map[string]string{"name": name})
	req, _ := http.NewRequest("POST", "https://ollama.com/api/show", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var info struct {
		ModelInfo    map[string]any `json:"model_info"`
		Capabilities []string       `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil
	}

	meta := &llm.ModelMeta{ID: name, MaxTokens: 32000}
	for k, v := range info.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if f, ok := v.(float64); ok {
				meta.ContextWindow = int(f)
			}
		}
	}
	if meta.ContextWindow == 0 {
		meta.ContextWindow = llm.InferContextWindow(name)
	}
	for _, cap := range info.Capabilities {
		switch cap {
		case "vision":
			meta.Vision = true
		case "thinking":
			meta.Thinking = true
		}
	}
	llm.ApplyRegistryPricing(meta)
	return meta
}
