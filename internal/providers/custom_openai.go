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

// CustomOpenAI implements Provider for a user-configured, OpenAI Chat
// Completions-compatible endpoint (a proxy, gateway, or self-hosted
// server) — the "type": "openai" case of types.CustomProvider. It is
// OpenAI's structural twin: same credential handling, same streaming
// dialect (llm.DoOpenAIStream), but every network-facing detail (name,
// base URL, models URL, extra headers) comes from configuration instead
// of being hardcoded, and — critically — FetchModels here is a genuinely
// separate code path from OpenAI's own, NOT a parametrized reuse of it.
// See fetchCustomOpenAIModels' doc comment for why that separation matters.
type CustomOpenAI struct {
	name        string
	displayName string
	baseURL     string
	modelsURL   string // "" means derive baseURL+"/models" at fetch time
	headers     map[string]string
	apiKey      string
	client      *http.Client
	cache       map[string]types.ModelMeta
	mu          sync.RWMutex

	// fetchModelsFn, if set, REPLACES the default HTTP-GET-<url>/models
	// discovery in FetchModels — the hook RegisterOpenAI (the SDK's
	// programmatic registration path, via agent.NewOpenAIProvider) exposes
	// as ProviderWithFetchModels. nil for every settings.json-configured
	// custom provider (types.CustomProvider carries no such field — it has
	// no JSON representation, so it can only ever come from Go code).
	fetchModelsFn func(apiKey string) ([]types.ModelMeta, error)
}

// NewCustomOpenAI builds a CustomOpenAI provider named name from cfg.
// Credentials are resolved eagerly (mirroring NewOpenAI/NewAnthropic),
// keyed under name in the credentials store — never an environment
// variable (there's no well-known env var name to invent per arbitrary
// custom provider, unlike the built-ins).
func NewCustomOpenAI(name string, cfg types.CustomProvider) *CustomOpenAI {
	o := &CustomOpenAI{
		name:        name,
		displayName: cfg.Display,
		baseURL:     cfg.URL,
		modelsURL:   cfg.ModelsURL,
		headers:     cfg.Headers,
		client:      &http.Client{},
		cache:       make(map[string]types.ModelMeta),
	}
	o.ResolveCredentials() //nolint:errcheck
	return o
}

func (o *CustomOpenAI) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (o *CustomOpenAI) ResolveCredentials() (types.Credentials, error) {
	if o.apiKey != "" {
		return types.APIKeyCredentials(o.apiKey), nil
	}
	// No env var fallback — see NewCustomOpenAI's doc comment.
	if v, src := resolveAPIKey(o.name, ""); src != ActivationNone {
		o.apiKey = v
		return types.APIKeyCredentials(v), nil
	}
	return types.Credentials{}, fmt.Errorf("no credentials found")
}

func (o *CustomOpenAI) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("%s expects api_key credentials, got %s", o.name, creds.Type)
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
	return storeAPIKey(o.name, creds.APIKey)
}

func (o *CustomOpenAI) Disconnect() error {
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	o.apiKey = ""
	return deleteCredential(o.name)
}

func (o *CustomOpenAI) Name() string { return o.name }

// DisplayName returns the configured Display, falling back to the same
// name Name() returns — same fallback idiom as ModelsURL falling back to
// baseURL+"/models".
func (o *CustomOpenAI) DisplayName() string {
	if o.displayName != "" {
		return o.displayName
	}
	return o.name
}

func (o *CustomOpenAI) Description() string { return describeState(o) }

func (o *CustomOpenAI) ActivationSource() ActivationSource {
	_, src := resolveAPIKey(o.name, "")
	return src
}

func (o *CustomOpenAI) IsActive() bool {
	_, err := o.ResolveCredentials()
	return err == nil
}

func (o *CustomOpenAI) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *CustomOpenAI) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (o *CustomOpenAI) FetchModels() ([]types.ModelMeta, error) {
	var metas []types.ModelMeta
	var err error
	if o.fetchModelsFn != nil {
		metas, err = o.fetchModelsFn(o.apiKey)
	} else {
		metas, err = fetchCustomOpenAIModels(o.name, o.apiKey, o.baseURL, o.modelsURL, o.headers)
	}
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

// fetchCustomOpenAIModels lists models from a user-configured OpenAI-dialect
// endpoint. Deliberately a STANDALONE function, not a parametrized call
// into fetchOpenAIModels — that keeps it structurally impossible for a
// future edit to the real OpenAI fetch path to accidentally leak its
// isOpenAIChatModel prefix filter (gpt-/o1/o3/o4/chatgpt-) onto this one.
// That filter exists only because the REAL OpenAI API's /v1/models mixes
// chat models with embeddings/audio/image models and offers no capability
// field to tell them apart. A custom provider has no such guarantee about
// its model names — e.g. a proxy fronting MiniMax or Llama models would
// return zero results if OpenAI's naming convention were imposed on it —
// so every model ID the endpoint returns is accepted as-is. No standard
// capability field exists across OpenAI-compatible proxies to filter on
// instead (checked live: LiteLLM, Lemonade, and others each invent their
// own non-standardized extension, no shared contract), so accepting
// everything is the correct default for arbitrary user-configured
// endpoints, not a shortcut.
func fetchCustomOpenAIModels(providerName, apiKey, baseURL, modelsURL string, headers map[string]string) ([]types.ModelMeta, error) {
	url := modelsURL
	if url == "" {
		url = baseURL + "/models"
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, types.NewProviderAPIError(providerName, resp.StatusCode, nil)
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
		metas = append(metas, llm.EnrichMeta(types.ModelMeta{ID: m.ID}))
	}
	return metas, nil
}

func (o *CustomOpenAI) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.baseURL+"/chat/completions", o.apiKey, &llm.OpenAIRequest{Request: req}, o.headers, cb)
}
