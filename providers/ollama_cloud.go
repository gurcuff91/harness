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

const (
	ollamaCloudAPIKeyCred    = "ollama-cloud.api_key"
	ollamaCloudAPIKeyEnv     = "OLLAMA_CLOUD_API_KEY"
	ollamaCloudURLSettingKey = "ollama-cloud.url"
	ollamaCloudURLEnv        = "OLLAMA_CLOUD_URL"
	ollamaCloudURLDefault    = "https://ollama.com/v1"
)

func getOllamaCloudURL() string {
	if v := os.Getenv(ollamaCloudURLEnv); v != "" {
		return v
	}
	if v, ok := config.GetSettingsManager().Get(ollamaCloudURLSettingKey); ok && v != "" {
		return v
	}
	return ollamaCloudURLDefault
}

type OllamaCloud struct {
	apiKey  string
	baseURL string
	client  *http.Client
	cache   map[string]types.ModelMeta
	mu      sync.RWMutex
}

func NewOllamaCloud() *OllamaCloud {
	o := &OllamaCloud{
		client: &http.Client{},
		cache:  make(map[string]types.ModelMeta),
	}
	o.baseURL = getOllamaCloudURL()
	o.ResolveCredentials() //nolint:errcheck
	return o
}

func (o *OllamaCloud) Name() string { return "ollama-cloud" }
func (o *OllamaCloud) ActivationSource() ActivationSource {
	return activationSourceAPIKey(ollamaCloudAPIKeyEnv, ollamaCloudAPIKeyCred)
}
func (o *OllamaCloud) IsActive() bool {
	_, err := o.ResolveCredentials()
	return err == nil
}

func (o *OllamaCloud) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (o *OllamaCloud) ResolveCredentials() (types.Credentials, error) {
	return resolveAPIKey(&o.apiKey, ollamaCloudAPIKeyEnv, ollamaCloudAPIKeyCred)
}

func (o *OllamaCloud) SaveCredentials(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("ollama-cloud expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}
	if err := saveAPIKey(&o.apiKey, ollamaCloudAPIKeyCred, creds.APIKey); err != nil {
		return err
	}
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	if _, err := o.FetchModels(); err != nil {
		_ = o.ClearCredentials()
		return fmt.Errorf("invalid credentials: %w", err)
	}
	return nil
}

func (o *OllamaCloud) ClearCredentials() error {
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	return clearAPIKey(&o.apiKey, ollamaCloudAPIKeyCred)
}

func (o *OllamaCloud) Connect(creds types.Credentials) error { return o.SaveCredentials(creds) }
func (o *OllamaCloud) Disconnect() error                     { return o.ClearCredentials() }

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

func (o *OllamaCloud) FetchModels() ([]types.ModelMeta, error) {
	// Validate API key first — /models is public on ollama.com, doesn't require auth
	if !o.validateKey() {
		return nil, fmt.Errorf("invalid API key")
	}
	req, _ := http.NewRequest("GET", o.baseURL+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("API error (status %d)", resp.StatusCode)
	}
	defer resp.Body.Close()

	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("failed to parse models response")
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
	return o.Models(), nil
}

// validateKey checks the API key by hitting an auth-required endpoint.
func (o *OllamaCloud) validateKey() bool {
	body := strings.NewReader(`{"model":"gemma3:4b","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
	req, _ := http.NewRequest("POST", o.baseURL+"/chat/completions", body)
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	// 401/403 = invalid key; anything else (200, 400, 429) = key is valid
	return resp.StatusCode != 401 && resp.StatusCode != 403
}

func (o *OllamaCloud) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.baseURL+"/chat/completions", o.apiKey, &llm.OpenAIRequest{Request: req}, nil, cb)
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
