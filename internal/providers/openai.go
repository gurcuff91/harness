package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	llm "github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// OpenAI implements Provider for the OpenAI API.
type OpenAI struct {
	apiKey  string
	baseURL string
	client  *http.Client
	cache   map[string]types.ModelMeta
	mu      sync.RWMutex
}

const (
	openAIAPIKeyEnv = "OPENAI_API_KEY"
)

func NewOpenAI() *OpenAI {
	o := &OpenAI{
		baseURL: "https://api.openai.com/v1",
		client:  &http.Client{},
		cache:   make(map[string]types.ModelMeta),
	}
	o.ResolveCredentials() //nolint:errcheck
	return o
}

func (o *OpenAI) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (o *OpenAI) ResolveCredentials() (types.Credentials, error) {
	if o.apiKey != "" {
		return types.APIKeyCredentials(o.apiKey), nil
	}
	if v, src := resolveAPIKey("openai", openAIAPIKeyEnv); src != ActivationNone {
		o.apiKey = v
		return types.APIKeyCredentials(v), nil
	}
	return types.Credentials{}, fmt.Errorf("no credentials found")
}

func (o *OpenAI) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("openai expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}

	o.apiKey = creds.APIKey
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	if _, err := o.FetchModels(); err != nil {
		o.apiKey = ""
		return fmt.Errorf("invalid credentials: %w", err)
	}
	return storeAPIKey("openai", creds.APIKey)
}

func (o *OpenAI) Disconnect() error {
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	o.apiKey = ""
	return deleteCredential("openai")
}

func NewOpenAIWithConfig(apiKey, baseURL string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{},
		cache:   make(map[string]types.ModelMeta),
	}
}

func (o *OpenAI) Name() string        { return "openai" }
func (o *OpenAI) DisplayName() string { return "OpenAI" }
func (o *OpenAI) Description() string { return describeState(o) }
func (o *OpenAI) ActivationSource() ActivationSource {
	_, src := resolveAPIKey("openai", openAIAPIKeyEnv)
	return src
}
func (o *OpenAI) IsActive() bool {
	_, err := o.ResolveCredentials()
	return err == nil
}

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

func (o *OpenAI) FetchModels() ([]types.ModelMeta, error) {
	metas, err := fetchOpenAIModels(o.apiKey, o.baseURL)
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

// Allowed model prefixes for chat/reasoning models we care about.
var openAIModelPrefixes = []string{"gpt-", "o1", "o3", "o4", "chatgpt-"}

func fetchOpenAIModels(apiKey, baseURL string) ([]types.ModelMeta, error) {
	req, err := http.NewRequest("GET", baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, types.NewProviderAPIError("openai", resp.StatusCode, nil)
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse models response")
	}

	var metas []types.ModelMeta
	for _, m := range result.Data {
		if !isOpenAIChatModel(m.ID) {
			continue
		}
		meta := llm.EnrichMeta(types.ModelMeta{ID: m.ID})
		metas = append(metas, meta)
	}
	return metas, nil
}

func isOpenAIChatModel(id string) bool {
	for _, p := range openAIModelPrefixes {
		if len(id) >= len(p) && id[:len(p)] == p {
			return true
		}
	}
	return false
}

func (o *OpenAI) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.baseURL+"/chat/completions", o.apiKey, &llm.OpenAIRequest{Request: req}, nil, cb)
}
