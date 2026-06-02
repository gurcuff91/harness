package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

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

const (
	openCodeGoAPIKeyCred = "opencode-go.api_key"
	openCodeGoAPIKeyEnv  = "OPENCODE_GO_API_KEY"
)

func NewOpenCodeGo() *OpenCodeGo {
	o := &OpenCodeGo{
		client: &http.Client{},
		cache:  make(map[string]types.ModelMeta),
	}
	return o
}

func (o *OpenCodeGo) Name() string { return "opencode-go" }
func (o *OpenCodeGo) ActivationSource() ActivationSource {
	return activationSourceAPIKey(openCodeGoAPIKeyEnv, openCodeGoAPIKeyCred)
}
func (o *OpenCodeGo) IsActive() bool {
	_, err := o.ResolveCredentials()
	return err == nil
}

func (o *OpenCodeGo) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (o *OpenCodeGo) ResolveCredentials() (types.Credentials, error) {
	return resolveAPIKey(&o.apiKey, openCodeGoAPIKeyEnv, openCodeGoAPIKeyCred)
}

func (o *OpenCodeGo) SaveCredentials(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("opencode-go expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}
	if err := saveAPIKey(&o.apiKey, openCodeGoAPIKeyCred, creds.APIKey); err != nil {
		return err
	}
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	_, _ = o.FetchModels()
	return nil
}

func (o *OpenCodeGo) ClearCredentials() error {
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	return clearAPIKey(&o.apiKey, openCodeGoAPIKeyCred)
}

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

func (o *OpenCodeGo) FetchModels() ([]types.ModelMeta, error) {
	// /models is public on opencode.ai — validate key first via chat endpoint
	if !o.validateKey() {
		return nil, fmt.Errorf("invalid API key")
	}
	metas, err := fetchOpenCodeGoModels(o.apiKey)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		o.cache[m.ID] = m
	}
	o.mu.Unlock()
	return metas, nil
}

func (o *OpenCodeGo) validateKey() bool {
	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
	req, _ := http.NewRequest("POST", openCodeGoURL+"/chat/completions", body)
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode != 401 && resp.StatusCode != 403
}

func (o *OpenCodeGo) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, openCodeGoURL+"/chat/completions", o.apiKey, &llm.OpenAIRequest{Request: req}, nil, cb)
}

func fetchOpenCodeGoModels(apiKey string) ([]types.ModelMeta, error) {
	req, _ := http.NewRequest("GET", openCodeGoURL+"/models", nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d)", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse models response")
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
	return metas, nil
}
