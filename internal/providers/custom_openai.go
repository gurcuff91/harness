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
// OpenAI's structural twin for the wire dialect (same streaming code,
// llm.DoOpenAIStream), but every network-facing detail (name, base URL,
// models URL, extra headers) comes from configuration instead of being
// hardcoded, and — critically — FetchModels here is a genuinely separate
// code path from OpenAI's own, NOT a parametrized reuse of it. See
// fetchCustomOpenAIModels' doc comment for why that separation matters.
//
// Authentication is ALWAYS via Headers, never a separate API key — there
// is no way to guess, for an arbitrary custom backend, whether it expects
// "Authorization: Bearer <token>", a custom header ("X-Api-Key: ..."), or
// several headers combined (a real field example: a gateway needing
// X-Api-Key + X-Actor + X-Card-Id together). Forcing a single well-known
// "the API key" concept onto that would be actively wrong for backends
// that don't use Bearer auth at all. So CustomOpenAI carries no apiKey
// field, sends no automatic Authorization header, and treats itself as
// always-active — structurally identical to how auto-detected Ollama
// (CredTypeNone, Connect/Disconnect always rejected) already models "this
// provider's activation isn't credential-based" for a different reason
// (a local ping instead of headers). Both the declarative (settings.json)
// and programmatic (agent.NewOpenAIProvider) registration paths land here
// identically — there is no per-path branching in this file at all.
type CustomOpenAI struct {
	name        string
	displayName string
	baseURL     string
	modelsURL   string // "" means derive baseURL+"/models" at fetch time
	headers     map[string]string
	client      *http.Client
	cache       map[string]types.ModelMeta
	mu          sync.RWMutex

	// fetchModelsFn, if set, REPLACES the default HTTP-GET-<url>/models
	// discovery in FetchModels — the hook RegisterOpenAI (the SDK's
	// programmatic registration path, via agent.NewOpenAIProvider) exposes
	// as ProviderWithFetchModels. nil for every settings.json-configured
	// custom provider (types.CustomProvider carries no such field — it has
	// no JSON representation, so it can only ever come from Go code). Any
	// authentication the hook needs is the caller's own closure capture —
	// there's no apiKey parameter to thread through (see the type's own
	// doc comment for why).
	fetchModelsFn func() ([]types.ModelMeta, error)
}

// NewCustomOpenAI builds a CustomOpenAI provider named name from cfg.
func NewCustomOpenAI(name string, cfg types.CustomProvider) *CustomOpenAI {
	return &CustomOpenAI{
		name:        name,
		displayName: cfg.Display,
		baseURL:     cfg.URL,
		modelsURL:   cfg.ModelsURL,
		headers:     cfg.Headers,
		client:      &http.Client{},
		cache:       make(map[string]types.ModelMeta),
	}
}

// CredentialType is CredTypeNone — authentication is entirely via Headers,
// never a separate credential the CLI/API could manage. Mirrors Ollama's
// own CredTypeNone for the same underlying reason: this provider's
// activation isn't gated by anything Connect/Disconnect could meaningfully
// mutate.
func (o *CustomOpenAI) CredentialType() types.CredentialType { return types.CredTypeNone }

// ResolveCredentials always succeeds with CredTypeNone — there is nothing
// to resolve. Mirrors Ollama.ResolveCredentials exactly.
func (o *CustomOpenAI) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: types.CredTypeNone}, nil
}

// Connect is not supported — a custom provider's authentication is fixed
// at registration time (via Headers, either settings.json's "headers" or
// agent.ProviderWithHeaders), not something `harness connect` can
// meaningfully change. Mirrors Ollama.Connect's exact rejection pattern.
func (o *CustomOpenAI) Connect(_ types.Credentials) error {
	return fmt.Errorf("%s authenticates entirely via its configured headers — connect/disconnect not applicable", o.name)
}

// Disconnect is not supported — see Connect's doc comment.
func (o *CustomOpenAI) Disconnect() error {
	return fmt.Errorf("%s authenticates entirely via its configured headers — connect/disconnect not applicable", o.name)
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

// ActivationSource is always ActivationAuto — same reasoning as
// CredentialType: nothing about this provider's activation is
// credential-chain-driven (env var vs. credentials.json), it's simply
// always on once registered. Mirrors Ollama.ActivationSource's shape
// (though Ollama's is conditional on a live ping; this one has no
// analogous "is it actually reachable" check, so it's unconditional).
func (o *CustomOpenAI) ActivationSource() ActivationSource { return ActivationAuto }

// IsActive is always true — see the type's own doc comment for why there
// is no credential to be missing.
func (o *CustomOpenAI) IsActive() bool { return true }

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
		metas, err = o.fetchModelsFn()
	} else {
		metas, err = fetchCustomOpenAIModels(o.name, o.baseURL, o.modelsURL, o.headers)
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
//
// No Authorization header is ever set here — headers carries whatever
// authentication (if any) the endpoint needs, exactly as configured. See
// CustomOpenAI's own doc comment for why there's no separate apiKey
// concept.
func fetchCustomOpenAIModels(providerName, baseURL, modelsURL string, headers map[string]string) ([]types.ModelMeta, error) {
	url := modelsURL
	if url == "" {
		url = baseURL + "/models"
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("invalid credentials")
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

// CompleteStream sends no Authorization header of its own — headers
// (extraHeaders below) carries whatever authentication the endpoint
// needs, exactly as configured. See the type's own doc comment.
//
// AllowCleanEOF is set unconditionally: an arbitrary custom endpoint may
// be a direct backend OR a proxy/gateway fronting one (the motivating
// case: a gateway forwarding to MiniMax, which closes its SSE response
// after a final finish_reason chunk without ever sending [DONE] — see
// internal/providers/minimax.go's own CompleteStream and commit
// b3f42d7's "fix: accept MiniMax clean SSE completion"). parseOpenAIStream
// only honors AllowCleanEOF when it actually observed a finish_reason
// chunk before the connection closed (sawTerminalChunk) — a connection
// that drops mid-response with no terminal chunk still errors exactly as
// before, so enabling this unconditionally here doesn't mask a genuinely
// dropped connection, it just stops penalizing backends (reachable only
// through this generic path, where the real dialect can't be known in
// advance) that legitimately omit [DONE].
func (o *CustomOpenAI) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.baseURL+"/chat/completions", "",
		&llm.OpenAIRequest{Request: req, AllowCleanEOF: true}, o.headers, cb)
}
