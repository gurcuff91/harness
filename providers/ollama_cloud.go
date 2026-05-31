package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

const ollamaCloudURL = "https://ollama.com/v1"

type OllamaCloud struct {
	apiKey string
	client *http.Client
	cache  map[string]types.ModelMeta
	mu     sync.RWMutex
}

const (
	ollamaCloudAPIKeyCred = "ollama-cloud.api_key"
	ollamaCloudAPIKeyEnv  = "OLLAMA_API_KEY"
)

func NewOllamaCloud() *OllamaCloud {
	apiKey := os.Getenv(ollamaCloudAPIKeyEnv)
	if apiKey == "" {
		apiKey, _ = config.LoadCred(ollamaCloudAPIKeyCred)
	}
	o := &OllamaCloud{
		apiKey: apiKey,
		client: &http.Client{},
		cache:  make(map[string]types.ModelMeta),
	}
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func (o *OllamaCloud) Name() string   { return "ollama-cloud" }
func (o *OllamaCloud) IsActive() bool { return o.apiKey != "" }

func (o *OllamaCloud) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (o *OllamaCloud) SetCredentials(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("ollama-cloud expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}
	o.mu.Lock()
	o.apiKey = creds.APIKey
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	config.StoreCred(ollamaCloudAPIKeyCred, creds.APIKey)
	o.FetchModels()
	return nil
}

func (o *OllamaCloud) ClearCredentials() error {
	o.mu.Lock()
	o.apiKey = ""
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	return config.DeleteCred(ollamaCloudAPIKeyCred)
}

func (o *OllamaCloud) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *OllamaCloud) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (o *OllamaCloud) FetchModels() []types.ModelMeta {
	req, _ := http.NewRequest("GET", ollamaCloudURL+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := o.client.Do(req)
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

	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta, len(list.Data))
	for _, item := range list.Data {
		meta := types.ModelMeta{
			ID:            item.ID,
			ContextWindow: llm.InferContextWindow(item.ID),
			MaxTokens:     32000,
			Vision:        llm.InferVision(item.ID),
		}
		// Enrich with /api/show capabilities
		if info := fetchOllamaCloudModelInfo(item.ID); info != nil {
			meta = *info
		}
		llm.ApplyRegistryPricing(&meta)
		o.cache[item.ID] = meta
	}
	o.mu.Unlock()
	return o.Models()
}

func (o *OllamaCloud) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.apiKey, ollamaCloudURL, req, nil, cb)
}




func fetchOllamaCloudModelInfo(name string) *types.ModelMeta {
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

	meta := &types.ModelMeta{ID: name, MaxTokens: 32000}
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
	return meta
}
