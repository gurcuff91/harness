package providers

import (
	"context"
	"fmt"
	"net/http"
	"sync"

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

const (
	openAIAPIKeyCred = "openai.api_key"
	openAIAPIKeyEnv  = "OPENAI_API_KEY"
)

func NewOpenAI() *OpenAI {
	o := &OpenAI{
		baseURL: "https://api.openai.com/v1",
		client:  &http.Client{},
		cache:   make(map[string]types.ModelMeta),
	}
	o.ResolveCredentials() //nolint:errcheck
	if o.IsActive() {
		o.FetchModels()
	}
	return o
}

func (o *OpenAI) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (o *OpenAI) ResolveCredentials() (types.Credentials, error) {
	return resolveAPIKey(&o.apiKey, openAIAPIKeyEnv, openAIAPIKeyCred)
}

func (o *OpenAI) SaveCredentials(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("openai expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}
	if err := saveAPIKey(&o.apiKey, openAIAPIKeyCred, creds.APIKey); err != nil {
		return err
	}
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	o.FetchModels()
	return nil
}

func (o *OpenAI) ClearCredentials() error {
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	return clearAPIKey(&o.apiKey, openAIAPIKeyCred)
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



