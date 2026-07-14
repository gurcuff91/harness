package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// minimaxURL is MiniMax's OpenAI-compatible base. Verified live:
//   - GET  /v1/models            → OpenAI-style {object:list, data:[...]}
//   - POST /v1/chat/completions  → OpenAI-style streaming + tool calls
//   - reasoning_split:true       → emits thinking via reasoning_content
const minimaxURL = "https://api.minimax.io/v1"

// MiniMax implements Provider for the MiniMax API (OpenAI-compatible).
// Thinking is requested via reasoning_split so the model returns its reasoning
// in the separate reasoning_content field instead of inline <think> tags.
type MiniMax struct {
	apiKey string
	client *http.Client
	cache  map[string]types.ModelMeta
	mu     sync.RWMutex
}

const (
	miniMaxAPIKeyEnv  = "MINIMAX_API_KEY"
)

func NewMiniMax() *MiniMax {
	m := &MiniMax{
		client: &http.Client{},
		cache:  make(map[string]types.ModelMeta),
	}
	m.ResolveCredentials() //nolint:errcheck
	return m
}

func (o *MiniMax) Name() string        { return "minimax" }
func (o *MiniMax) DisplayName() string { return "MiniMax" }
func (o *MiniMax) Description() string { return describeState(o) }

func (o *MiniMax) ActivationSource() ActivationSource {
	_, src := resolveAPIKey("minimax", miniMaxAPIKeyEnv)
	return src
}

func (o *MiniMax) IsActive() bool {
	_, err := o.ResolveCredentials()
	return err == nil
}

func (o *MiniMax) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (o *MiniMax) ResolveCredentials() (types.Credentials, error) {
	if o.apiKey != "" {
		return types.APIKeyCredentials(o.apiKey), nil
	}
	if v, src := resolveAPIKey("minimax", miniMaxAPIKeyEnv); src != ActivationNone {
		o.apiKey = v
		return types.APIKeyCredentials(v), nil
	}
	return types.Credentials{}, fmt.Errorf("no credentials found")
}

func (o *MiniMax) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("minimax expects api_key credentials, got %s", creds.Type)
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
	return storeAPIKey("minimax", creds.APIKey)
}

func (o *MiniMax) Disconnect() error {
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	o.apiKey = ""
	return deleteCredential("minimax")
}

func (o *MiniMax) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *MiniMax) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (o *MiniMax) FetchModels() ([]types.ModelMeta, error) {
	metas, err := fetchMiniMaxModels(o.apiKey)
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

func (o *MiniMax) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	// ReasoningSplit moves thinking into reasoning_content (parsed by the
	// shared OpenAI stream handler) rather than inline <think> tags.
	return llm.DoOpenAIStream(ctx, o.client, minimaxURL+"/chat/completions", o.apiKey,
		&llm.OpenAIRequest{Request: req, ReasoningSplit: true}, nil, cb)
}

// fetchMiniMaxModels lists models from MiniMax's /v1/models (OpenAI format) and
// enriches each via the standard cascade (OpenRouter → hardcode → inference).
// The API only returns model IDs, so all capabilities/pricing come from the
// cascade — MiniMax models are well covered by OpenRouter.
func fetchMiniMaxModels(apiKey string) ([]types.ModelMeta, error) {
	req, _ := http.NewRequest("GET", minimaxURL+"/models", nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
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
		metas = append(metas, llm.EnrichMeta(types.ModelMeta{ID: m.ID}))
	}
	return metas, nil
}
