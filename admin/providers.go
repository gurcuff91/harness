package admin

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/oauthflow"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// Providers administers the LLM providers of this process: listing, models,
// credentials (connect / disconnect / OAuth), quotas and custom providers.
// The registry and the credentials are process-global, so every running
// session sees a change at its next turn.
type Providers struct{ ad *Admin }

func newProviders(ad *Admin) *Providers { return &Providers{ad: ad} }

// ProviderInfo describes one registered provider.
type ProviderInfo struct {
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	Active         bool   `json:"active"`
	Activation     string `json:"activation"`
	IsSubscription bool   `json:"is_subscription"`
	// CredentialType is the authoritative kind of credential the provider needs:
	// "none" (auto-detected, e.g. ollama), "api_key" (secret), or "oauth".
	CredentialType string `json:"credential_type"`
	ModelCount     int    `json:"model_count"`
	// Error is why an ACTIVE provider has no models right now (its model fetch
	// failed). Omitted when there is nothing to report.
	Error string `json:"error,omitempty"`
	// Dialect is the wire dialect of the provider's native API.
	Dialect string `json:"dialect"`
}

// ProviderModels is one provider's slice of the model registry: its models, or
// the reason it has none right now.
type ProviderModels struct {
	// Provider is the provider's slug.
	Provider string
	// Active reports whether the provider has valid credentials / is reachable.
	Active bool
	// Models are the provider's models (nil when inactive or when Err is set).
	Models []types.ModelListing
	// Err is why an ACTIVE provider has no model list (its FetchModels
	// failed). Nil when the provider is inactive or the fetch worked.
	Err error
}

// ConnectCredentials is what a provider is connected with. The credential
// Type comes from the provider itself.
type ConnectCredentials struct {
	APIKey           string `json:"api_key,omitempty"`
	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
	AccountID        string `json:"account_id,omitempty"` // codex-oauth
}

func find(name string) providers.Provider {
	providers.EnsureRegistry()
	for _, p := range providers.All() {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// ListModels is the model registry per provider: for every registered provider
// (or only the one named, "" = all) its models and, for an active one that
// could not produce them, the error. Providers run concurrently and results
// keep registry order. An unknown name yields an empty result (no error): use
// Models/Model for the distinguished errors.
func (p *Providers) ListModels(provider string) []ProviderModels {
	all := providers.All() // snapshot: safe while providers are (un)registered
	if provider != "" {
		var one []providers.Provider
		for _, pr := range all {
			if pr.Name() == provider {
				one = append(one, pr)
			}
		}
		all = one
	}
	out := make([]ProviderModels, len(all))
	var wg sync.WaitGroup
	for i, pr := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pm := ProviderModels{Provider: pr.Name()}
			defer func() { out[i] = pm }()
			if pm.Active = pr.IsActive(); !pm.Active {
				return
			}
			models := pr.Models()
			if len(models) == 0 {
				var err error
				if models, err = pr.FetchModels(); err != nil {
					pm.Err = err
					return
				}
			}
			dialect := pr.Dialect()
			for _, m := range models {
				pm.Models = append(pm.Models, types.ModelListing{
					Provider:  pr.Name(),
					Model:     pr.Name() + "/" + m.ID,
					Dialect:   dialect,
					ModelMeta: m,
				})
			}
		}()
	}
	wg.Wait()
	return out
}

// List returns every registered provider.
func (p *Providers) List(ctx context.Context) ([]ProviderInfo, error) {
	providers.EnsureRegistry()
	byName := map[string]ProviderModels{}
	for _, pm := range p.ListModels("") {
		byName[pm.Provider] = pm
	}
	list := []ProviderInfo{}
	for _, pr := range providers.All() {
		pm := byName[pr.Name()]
		act := string(pr.ActivationSource())
		if act == "none" {
			act = "inactive"
		} else if act == "envvar" {
			act = "env"
		}
		info := ProviderInfo{
			Name:           pr.Name(),
			DisplayName:    pr.DisplayName(),
			Description:    pr.Description(),
			Active:         pr.IsActive(),
			Activation:     act,
			IsSubscription: pr.CredentialType() == types.CredTypeOAuth,
			CredentialType: string(pr.CredentialType()),
			ModelCount:     len(pm.Models),
			Dialect:        pr.Dialect(),
		}
		if pm.Err != nil {
			info.Error = pm.Err.Error()
		}
		list = append(list, info)
	}
	return list, nil
}

// Models lists the models of every active provider, flat — a provider that
// fails contributes nothing — or, when provider is not "", of that one provider
// with the errors the unfiltered call hides: ErrNotFound (unknown provider,
// 404), ErrNotConnected (409), ErrUpstream (its model fetch failed, 502).
func (p *Providers) Models(ctx context.Context, provider string) ([]types.ModelListing, error) {
	pms := p.ListModels(provider)
	if provider != "" {
		if err := checkListing(provider, pms); err != nil {
			return nil, err
		}
	}
	list := []types.ModelListing{}
	for _, pm := range pms {
		list = append(list, pm.Models...)
	}
	return list, nil
}

func checkListing(name string, pms []ProviderModels) error {
	switch {
	case len(pms) == 0:
		return markf(ErrNotFound, "provider %q not found", name)
	case !pms[0].Active:
		return markf(ErrNotConnected, "provider %q is not connected", name)
	case pms[0].Err != nil:
		return markf(ErrUpstream, "provider %q: could not list models: %v", name, pms[0].Err)
	}
	return nil
}

// Model returns one model of a provider by its provider-local id. ErrNotFound
// for an unknown provider or model, ErrNotConnected, ErrUpstream as Models.
func (p *Providers) Model(ctx context.Context, provider, id string) (types.ModelListing, error) {
	pms := p.ListModels(provider)
	if err := checkListing(provider, pms); err != nil {
		return types.ModelListing{}, err
	}
	for _, m := range pms[0].Models {
		if m.ID == id {
			return m, nil
		}
	}
	return types.ModelListing{}, markf(ErrNotFound, "model %q not found in provider %q", id, provider)
}

// Connect saves credentials for a provider, validates them by fetching its
// models and activates it; it returns how many models it offers. ErrNotFound
// for an unknown provider (404); any other error is the provider refusing the
// credentials (wrapped in ErrInvalid, 400). The cached quota reading of the
// previous account is dropped.
func (p *Providers) Connect(ctx context.Context, name string, c ConnectCredentials) (int, error) {
	target := find(name)
	if target == nil {
		return 0, markf(ErrNotFound, "provider not found: %s", name)
	}
	creds := types.Credentials{
		Type:             target.CredentialType(),
		APIKey:           c.APIKey,
		AccessToken:      c.AccessToken,
		RefreshToken:     c.RefreshToken,
		ExpiresAt:        c.ExpiresAt,
		SubscriptionType: c.SubscriptionType,
		AccountID:        c.AccountID,
	}
	if err := target.Connect(creds); err != nil {
		return 0, mark(ErrInvalid, err)
	}
	providers.ForgetQuota(name) // a reading of the previous account must not be served
	return len(target.Models()), nil
}

// CredentialType reports the kind of credential a provider needs.
func (p *Providers) CredentialType(name string) (types.CredentialType, error) {
	target := find(name)
	if target == nil {
		return "", markf(ErrNotFound, "provider not found: %s", name)
	}
	return target.CredentialType(), nil
}

// Disconnect clears a provider's credentials. ErrNotFound for an unknown
// provider; other failures wrap ErrInvalid (400).
func (p *Providers) Disconnect(name string) error {
	target := find(name)
	if target == nil {
		return markf(ErrNotFound, "provider not found: %s", name)
	}
	if err := target.Disconnect(); err != nil {
		return mark(ErrInvalid, err)
	}
	providers.ForgetQuota(name)
	return nil
}

// Quota returns the provider's subscription usage through the process-wide
// cache (60 s TTL; refresh forces a new call, at most one per 5 s). retryAfter
// is the Retry-After to advertise (zero = none) and is meaningful even with a
// non-nil error or reading. Errors: ErrNotFound (unknown provider, or one
// without quota / an account with none: 404), ErrNotConnected (409),
// ErrUpstream (502).
func (p *Providers) Quota(ctx context.Context, name string, refresh bool) (q *types.Quota, retryAfter time.Duration, err error) {
	pr := providers.Find(name)
	if pr == nil {
		return nil, 0, markf(ErrNotFound, "provider %q not found", name)
	}
	if _, ok := pr.(providers.QuotaReporter); !ok {
		return nil, 0, markf(ErrNotFound, "provider %q does not report a quota", name)
	}
	q, retry, err := providers.CachedQuota(ctx, name, refresh)
	switch {
	case err == nil:
		return q, retry, nil
	case errors.Is(err, providers.ErrNoUpstream):
		return nil, retry, markf(ErrNotConnected, "provider %q is not connected (or has no valid credentials): connect it first", name)
	case errors.Is(err, providers.ErrNoQuota):
		return nil, retry, markf(ErrNotFound, "provider %q has no quota for this account", name)
	default:
		return nil, retry, mark(ErrUpstream, err)
	}
}

// OAuthStart begins a provider's OAuth PKCE login: it returns the URL the user
// must open and the verifier to hand back to OAuthExchange. Nothing is kept
// between the two calls. ErrNotFound for a provider with no OAuth flow (404);
// ErrConflict when a login is already in progress (409, e.g. codex's callback
// port is busy).
func (p *Providers) OAuthStart(provider string) (authURL, verifier string, err error) {
	flow, err := oauthflow.For(provider)
	if err != nil {
		return "", "", mark(ErrNotFound, err)
	}
	authURL, verifier, err = flow.Start()
	if err != nil {
		return "", "", mark(ErrConflict, err)
	}
	return authURL, verifier, nil
}

// OAuthExchange swaps the code the user pasted back for credentials. It does
// NOT connect the provider: pass them to Connect. ErrNotFound for a provider
// with no OAuth flow; ErrInvalid when the verifier is empty (400); ErrUpstream
// when the exchange fails (502).
func (p *Providers) OAuthExchange(provider, code, verifier string) (*types.Credentials, error) {
	flow, err := oauthflow.For(provider)
	if err != nil {
		return nil, mark(ErrNotFound, err)
	}
	if verifier == "" {
		return nil, markf(ErrInvalid, "verifier_code is required for exchange")
	}
	creds, err := flow.Exchange(code, verifier)
	if err != nil {
		return nil, mark(ErrUpstream, err)
	}
	return creds, nil
}

// ── Custom providers (settings.json "provider" collection) ──────────────

// ListCustom returns the custom providers declared in settings.
func (p *Providers) ListCustom() map[string]types.CustomProvider {
	return config.GetSettingsManager().CustomProviders()
}

// PutCustom stores (or replaces) a custom provider declaration. An invalid
// one (type, url, reserved name) is ErrUnprocessable (422). It takes effect on
// ReloadCustom.
func (p *Providers) PutCustom(name string, cp types.CustomProvider) error {
	if err := config.GetSettingsManager().SetCustomProvider(name, cp); err != nil {
		if errors.Is(err, config.ErrInvalidCustomProvider) {
			return mark(ErrUnprocessable, err)
		}
		return err
	}
	return nil
}

// DeleteCustom removes a custom provider declaration (ErrNotFound if absent).
func (p *Providers) DeleteCustom(name string) error {
	sm := config.GetSettingsManager()
	if _, ok := sm.CustomProvider(name); !ok {
		return markf(ErrNotFound, "custom provider not found: %s", name)
	}
	return sm.DeleteCustomProvider(name)
}

// ReloadCustom applies the settings "provider" collection live. Entries whose
// name a programmatic provider already uses are skipped: the rest is applied
// and the returned error wraps ErrProviderNameConflict (the server answers 200
// "reloaded_with_conflicts" with its text). Built-in and programmatic providers
// are untouched; unchanged entries keep their provider and model cache.
func (p *Providers) ReloadCustom() error { return providers.ReloadCustom() }

// ── Programmatic registration (SDK) ─────────────────────────────────────

type customProviderConfig struct {
	display        string
	headers        map[string]string
	reasoningSplit bool
	fetchModels    func() ([]types.ModelMeta, error)
}

// CustomProviderOption configures RegisterOpenAI / RegisterAnthropic.
type CustomProviderOption func(*customProviderConfig)

// ProviderWithDisplay sets the human-friendly display name shown in listings
// (default: the provider's name).
func ProviderWithDisplay(display string) CustomProviderOption {
	return func(c *customProviderConfig) { c.display = display }
}

// ProviderWithHeaders sets the HTTP headers sent on every request the provider
// makes, model listing included. This is where ANY authentication goes: a
// custom provider has no API-key concept of its own.
func ProviderWithHeaders(headers map[string]string) CustomProviderOption {
	return func(c *customProviderConfig) { c.headers = headers }
}

// ProviderWithReasoningSplit sends "reasoning_split": true on every
// chat-completions request (OpenAI dialect only; ignored for Anthropic). Needed
// for a MiniMax-compatible backend; leave it off for everything else.
func ProviderWithReasoningSplit() CustomProviderOption {
	return func(c *customProviderConfig) { c.reasoningSplit = true }
}

// ProviderWithFetchModels replaces model discovery entirely (default: GET
// <url>/models). Capture any credentials in the closure.
func ProviderWithFetchModels(fn func() ([]types.ModelMeta, error)) CustomProviderOption {
	return func(c *customProviderConfig) { c.fetchModels = fn }
}

func build(opts []CustomProviderOption) customProviderConfig {
	var cfg customProviderConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return cfg
}

// RegisterOpenAI registers an OpenAI Chat Completions-compatible provider for
// this PROCESS, usable by any session as "name/model". The name must not be a
// built-in's or already registered. Authentication is ONLY headers
// (ProviderWithHeaders): a custom provider has no API-key concept. It may be
// called at any time, even while agents run, and paired with
// UnregisterProvider to replace a provider live. The registry is
// process-global, so this is a package function; Providers.RegisterOpenAI is
// the same thing as a method.
func RegisterOpenAI(name, url string, opts ...CustomProviderOption) error {
	cfg := build(opts)
	return providers.RegisterOpenAI(name, url, cfg.display, cfg.headers, cfg.reasoningSplit, cfg.fetchModels)
}

// RegisterAnthropic is RegisterOpenAI for an Anthropic Messages-compatible
// endpoint. url is the API root WITHOUT "/v1": "/v1/messages" and
// "/v1/models" are appended. Pointed at another harness instance's
// "/api/providers/<name>/proxy" it shares that instance's subscription with no
// credential at all (the proxy applies its own). ProviderWithReasoningSplit is
// ignored for this dialect.
func RegisterAnthropic(name, url string, opts ...CustomProviderOption) error {
	cfg := build(opts)
	return providers.RegisterAnthropic(name, url, cfg.display, cfg.headers, cfg.fetchModels)
}

// UnregisterProvider removes a provider registered with RegisterOpenAI or
// RegisterAnthropic. ErrProviderNotFound for an unknown name;
// ErrProviderNotRemovable for a built-in or settings-declared one.
func UnregisterProvider(name string) error { return providers.Unregister(name) }

// ReloadCustomProviders applies the settings "provider" collection live (see
// Providers.ReloadCustom).
func ReloadCustomProviders() error { return providers.ReloadCustom() }

// RegisterOpenAI is the method form of the package function.
func (p *Providers) RegisterOpenAI(name, url string, opts ...CustomProviderOption) error {
	return RegisterOpenAI(name, url, opts...)
}

// RegisterAnthropic is the method form of the package function.
func (p *Providers) RegisterAnthropic(name, url string, opts ...CustomProviderOption) error {
	return RegisterAnthropic(name, url, opts...)
}

// Unregister is the method form of UnregisterProvider.
func (p *Providers) Unregister(name string) error { return UnregisterProvider(name) }
