package providers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	llm "github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// CustomAnthropic implements Provider for a user-configured Anthropic Messages-
// compatible endpoint — the "type": "anthropic" case of types.CustomProvider.
// It is the structural twin of CustomOpenAI for the Anthropic dialect: the same
// streaming code as the built-in `anthropic` provider (llm.DoAnthropicStream),
// but every network-facing detail (base URL, models URL, headers) comes from
// configuration instead of being hardcoded.
//
// The motivating use is SHARING a provider between harness instances: instance
// A connects `claude-oauth` and serves it; instance B declares a custom
// anthropic provider whose URL is A's proxy root
// (http://A/api/providers/claude-oauth/proxy) and uses it like the native one.
// B therefore sends a PLAIN Anthropic Messages request — no Claude Code identity
// block, no tool-name mapping, no billing header — because A's proxy applies
// all of that when it forwards (see rewriteClaudeOAuthBody) and maps the tool
// names back in the response. Applying them in B too would be wrong: B has no
// OAuth token and must not pretend to be Claude Code.
//
// Authentication is ALWAYS via Headers, exactly like CustomOpenAI: there is no
// API-key field, no automatic x-api-key or Authorization header, and the
// provider is always active. (Pointed at another harness's proxy it needs no
// credential at all — the proxy drops whatever it is sent.) See CustomOpenAI
// for the full reasoning.
//
// anthropic-version is the one default it supplies ("2023-06-01"), because the
// Messages API rejects a request without it and a header the user configured
// always wins.
type CustomAnthropic struct {
	customMeta

	name        string
	displayName string
	baseURL     string
	modelsURL   string // "" = <baseURL>/v1/models
	headers     map[string]string
	client      *http.Client
	cache       map[string]types.ModelMeta
	mu          sync.RWMutex

	// fetchModelsFn, if set, REPLACES the default GET <models URL> discovery —
	// the hook agent.NewAnthropicProvider exposes as ProviderWithFetchModels.
	fetchModelsFn func() ([]types.ModelMeta, error)
}

var _ Provider = (*CustomAnthropic)(nil)

// NewCustomAnthropic builds a CustomAnthropic named name from a settings entry.
func NewCustomAnthropic(name string, cfg types.CustomProvider) *CustomAnthropic {
	return &CustomAnthropic{
		customMeta:  customMeta{fromSettings: true, settingsCfg: cfg},
		name:        name,
		displayName: cfg.Display,
		baseURL:     cfg.URL,
		modelsURL:   cfg.ModelsURL,
		headers:     cfg.Headers,
		client:      llm.NewProviderClient(0),
		cache:       make(map[string]types.ModelMeta),
	}
}

func (o *CustomAnthropic) Name() string { return o.name }

func (o *CustomAnthropic) DisplayName() string {
	if o.displayName != "" {
		return o.displayName
	}
	return o.name
}

func (o *CustomAnthropic) Description() string { return describeState(o) }

// Dialect implements Provider.
func (o *CustomAnthropic) Dialect() string { return DialectAnthropic }

// CredentialType is CredTypeNone — authentication is entirely via Headers.
func (o *CustomAnthropic) CredentialType() types.CredentialType { return types.CredTypeNone }

func (o *CustomAnthropic) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: types.CredTypeNone}, nil
}

func (o *CustomAnthropic) ActivationSource() ActivationSource { return ActivationAuto }

// IsActive is always true — there is no credential to be missing.
func (o *CustomAnthropic) IsActive() bool { return true }

// Connect is not supported — see CustomOpenAI.Connect.
func (o *CustomAnthropic) Connect(_ types.Credentials) error {
	return fmt.Errorf("%s authenticates entirely via its configured headers — connect/disconnect not applicable", o.name)
}

// Disconnect is not supported — see Connect.
func (o *CustomAnthropic) Disconnect() error {
	return fmt.Errorf("%s authenticates entirely via its configured headers — connect/disconnect not applicable", o.name)
}

func (o *CustomAnthropic) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *CustomAnthropic) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

// stampHeaders sets the headers every request to the endpoint carries: the
// anthropic-version default, then the configured headers (which win).
func (o *CustomAnthropic) stampHeaders(h http.Header) {
	h.Set("anthropic-version", "2023-06-01")
	for k, v := range o.headers {
		h.Set(k, v)
	}
}

// FetchModels lists the endpoint's models with the capabilities, context and
// output limits it reports (the same parsing as the built-in provider, so a
// model served by another harness keeps its vision / thinking / effort data),
// following the API's pagination. Every model the endpoint lists is accepted:
// unlike Anthropic's own catalog, an arbitrary endpoint's ids need not start
// with "c".
func (o *CustomAnthropic) FetchModels() ([]types.ModelMeta, error) {
	var metas []types.ModelMeta
	var err error
	if o.fetchModelsFn != nil {
		metas, err = o.fetchModelsFn()
	} else {
		base := strings.TrimRight(o.baseURL, "/")
		if o.modelsURL != "" {
			// A custom models URL is used as given, first page only: it is an
			// explicit override for endpoints that don't follow /v1/models.
			metas, err = fetchAnthropicModelsAt(o.modelsURL, o.name, nil, func(req *http.Request, _ int) { o.stampHeaders(req.Header) })
		} else {
			metas, err = fetchAnthropicModelsFrom(base, o.name, nil, func(req *http.Request, _ int) { o.stampHeaders(req.Header) }, 1)
		}
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

// CompleteStream is the plain Anthropic dialect: no OAuth identity, no tool
// renaming — see the type's doc comment for why. Thinking is resolved from the
// model's own reported capabilities, as the built-in provider does.
func (o *CustomAnthropic) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	thinking := llm.BuildAnthropicThinkingFromMeta(o.ModelMeta(req.Model), req.ThinkingLevel, req.MaxTokens)
	extra := map[string]string{}
	// Interleaved-thinking beta only for non-adaptive models (as anthropic.go).
	if req.ThinkingLevel != "" && req.ThinkingLevel != "off" && thinking.OutputConfig == nil {
		extra["anthropic-beta"] = "interleaved-thinking-2025-05-14"
	}
	// Configured headers win over the beta we add, but a configured
	// anthropic-beta is MERGED with it rather than lost.
	if cfgBeta := o.headerValue("anthropic-beta"); cfgBeta != "" {
		extra["anthropic-beta"] = mergeBetaTokens(cfgBeta, extra["anthropic-beta"])
	}
	for k, v := range o.headers {
		if http.CanonicalHeaderKey(k) == "Anthropic-Beta" {
			continue // merged above
		}
		extra[k] = v
	}
	url := strings.TrimRight(o.baseURL, "/") + "/v1/messages"
	return llm.DoAnthropicStream(ctx, o.client, url, "", &llm.AnthropicRequest{Request: req, ThinkingConfig: &thinking}, extra, cb)
}

// headerValue returns the configured header named name, case-insensitively.
func (o *CustomAnthropic) headerValue(name string) string {
	for k, v := range o.headers {
		if http.CanonicalHeaderKey(k) == http.CanonicalHeaderKey(name) {
			return v
		}
	}
	return ""
}

// Upstream implements Provider: the declared endpoint with its static headers —
// what lets a harness instance chain: C → B (custom anthropic) → A (claude-oauth).
// B forwards C's plain request to A, where the identity rewrite happens once.
func (o *CustomAnthropic) Upstream() (Upstream, error) {
	h := make(map[string]string, len(o.headers))
	for k, v := range o.headers {
		h[k] = v
	}
	return Upstream{
		BaseURL:  strings.TrimRight(o.baseURL, "/"),
		Headers:  h,
		Defaults: map[string]string{"anthropic-version": "2023-06-01"},
		Allowed:  anthropicAllow,
		Client:   o.client,
	}, nil
}

// customMeta is the bookkeeping the registry keeps about a custom provider, shared
// by every custom type so ReloadCustom and Unregister need no type switch.
type customMeta struct {
	// programmatic marks a provider registered from code (RegisterOpenAI /
	// RegisterAnthropic) — the only kind Unregister may remove.
	programmatic bool
	// fromSettings marks a provider declared in settings.json's "provider"
	// collection — the only kind ReloadCustom replaces. settingsCfg is the entry
	// it was built from: an unchanged entry keeps the same object (and model
	// cache) across reloads.
	fromSettings bool
	settingsCfg  types.CustomProvider
}

func (m *customMeta) meta() *customMeta { return m }

// customProvider is implemented by every custom provider type (CustomOpenAI,
// CustomAnthropic): the registry's view of one.
type customProvider interface {
	Provider
	meta() *customMeta
}
