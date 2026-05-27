package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
)

// Ollama wraps OpenAI for local Ollama instances.
type Ollama struct {
	*OpenAI
	baseURL      string
	cachedModels []llm.ModelMeta
}

func NewOllama() *Ollama {
	url := config.GetOllamaURL()
	o := &Ollama{
		OpenAI:  NewOpenAIWithConfig("", url+"/v1"),
		baseURL: url,
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func (o *Ollama) Name() string    { return "ollama" }
func (o *Ollama) IsActive() bool  { return OllamaAvailable() }

func (o *Ollama) Models() []llm.ModelMeta { return o.cachedModels }

func (o *Ollama) FetchModels() []llm.ModelMeta {
	o.cachedModels = fetchOllamaModels()
	return o.cachedModels
}

func (o *Ollama) CompleteStream(ctx context.Context, req *llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	return llm.DoOpenAIStream(ctx, o.OpenAI.client, o.OpenAI.apiKey, o.OpenAI.baseURL, req, nil, cb)
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

func fetchOllamaModels() []llm.ModelMeta {
	url := config.GetOllamaURL()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url + "/api/tags")
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

	var metas []llm.ModelMeta
	for _, m := range result.Models {
		meta := llm.ModelMeta{
			ID:          m.Name,
			DisplayName: fmt.Sprintf("%s (%s)", m.Name, m.Details.ParameterSize),
		}
		llm.ApplyRegistryPricing(&meta)
		metas = append(metas, meta)
	}
	return metas
}
