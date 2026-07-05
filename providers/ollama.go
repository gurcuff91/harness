package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

const (
	ollamaURLEnv     = "OLLAMA_URL"
	ollamaURLDefault = "http://localhost:11434"
)

// getOllamaURL resolves the Ollama base URL. The cascade lives HERE, in the
// provider (env → stored config → default) — the SettingsManager only stores the
// raw ProviderConfig; it knows nothing about Ollama.
func getOllamaURL() string {
	if v := os.Getenv(ollamaURLEnv); v != "" {
		return v
	}
	if cfg, ok := config.GetSettingsManager().Provider("ollama"); ok && cfg.URL != "" {
		return cfg.URL
	}
	return ollamaURLDefault
}

// Ollama wraps OpenAI-compatible streaming for local Ollama instances.
type Ollama struct {
	baseURL string
	client  *http.Client
	cache   map[string]types.ModelMeta
	mu      sync.RWMutex
}

func NewOllama() *Ollama {
	o := &Ollama{
		baseURL: getOllamaURL(),
		client:  &http.Client{},
		cache:   make(map[string]types.ModelMeta),
	}
	return o
}

func (o *Ollama) Name() string        { return "ollama" }
func (o *Ollama) DisplayName() string { return "Ollama" }
func (o *Ollama) Description() string { return describeState(o) }
func (o *Ollama) ActivationSource() ActivationSource {
	if OllamaAvailable() {
		return ActivationAuto
	}
	return ActivationNone
}
func (o *Ollama) IsActive() bool { return OllamaAvailable() }

func (o *Ollama) CredentialType() types.CredentialType { return types.CredTypeNone }

// ResolveCredentials — no-op, ollama is auto-detected via ping.
func (o *Ollama) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: types.CredTypeNone}, nil
}

// Connect — not supported for auto-detected providers.
func (o *Ollama) Connect(_ types.Credentials) error {
	return fmt.Errorf("ollama is auto-detected, cannot connect/disconnect manually")
}
func (o *Ollama) Disconnect() error {
	return fmt.Errorf("ollama is auto-detected, cannot connect/disconnect manually")
}

func (o *Ollama) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *Ollama) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (o *Ollama) FetchModels() ([]types.ModelMeta, error) {
	metas := fetchOllamaModels(o.baseURL)
	if metas == nil {
		return nil, fmt.Errorf("ollama unreachable")
	}
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		o.cache[m.ID] = m
	}
	o.mu.Unlock()
	return metas, nil
}

func (o *Ollama) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.baseURL+"/v1/chat/completions", "", &llm.OpenAIRequest{Request: req}, nil, cb)
}

func OllamaAvailable() bool {
	url := getOllamaURL()
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url + "/api/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func fetchOllamaModels(baseURL string) []types.ModelMeta {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(baseURL + "/api/tags")
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

	var metas []types.ModelMeta
	for _, m := range result.Models {
		meta := types.ModelMeta{
			ID:          m.Name,
			DisplayName: fmt.Sprintf("%s (%s)", m.Name, m.Details.ParameterSize),
		}
		// Ollama's /api/tags gives no context/caps — run the enrichment cascade
		// (OpenRouter → hardcode → inference) to fill the gaps. The ":cloud"/tag
		// suffix is handled by normalizeModelID so cloud-hosted models match.
		meta = llm.EnrichMeta(meta)
		metas = append(metas, meta)
	}
	return metas
}
