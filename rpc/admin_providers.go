package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/types"
)

func adminProvidersOps() []Op {
	return []Op{
		Define("admin/providers/list", "Lists every registered provider (no credentials).", opAdmProvidersList),
		Define("admin/providers/models", "Lists the models of every active provider, or of one (with the errors the unfiltered call hides).", opAdmProvidersModels),
		Define("admin/providers/model", "Returns one model of a provider by its provider-local id.", opAdmProvidersModel),
		Define("admin/providers/connect", "Saves a provider's credentials, validates them and activates it; answers how many models it offers.", opAdmProvidersConnect),
		Define("admin/providers/disconnect", "Clears a provider's credentials.", opAdmProvidersDisconnect),
		Define("admin/providers/quota", "Returns the provider's subscription usage (cached 60 s; refresh asks again, at most once per 5 s).", opAdmProvidersQuota),
		Define("admin/providers/oauth_start", "Begins a provider's OAuth login: answers the URL to open and the verifier to hand back.", opAdmProvidersOAuthStart),
		Define("admin/providers/oauth_exchange", "Swaps the code the user pasted for credentials (does not connect: pass them to connect).", opAdmProvidersOAuthExchange),
		Define("admin/providers/credential_type", "Reports the kind of credential a provider needs.", opAdmProvidersCredentialType),
		Define("admin/providers/list_custom", "Lists the custom providers declared in settings.", opAdmProvidersListCustom),
		Define("admin/providers/put_custom", "Declares (or replaces) a custom provider in settings; it takes effect on reload_custom.", opAdmProvidersPutCustom),
		Define("admin/providers/delete_custom", "Removes a custom provider declaration from settings.", opAdmProvidersDeleteCustom),
		Define("admin/providers/reload_custom", "Applies the settings' custom providers live. Names clashing with programmatic providers are skipped and reported, not an error.", opAdmProvidersReloadCustom),
		Define("admin/providers/register_openai", "Registers an OpenAI Chat Completions-compatible provider for this process (authentication is headers only).", opAdmProvidersRegisterOpenAI),
		Define("admin/providers/register_anthropic", "Registers an Anthropic Messages-compatible provider for this process (url without /v1).", opAdmProvidersRegisterAnthropic),
		Define("admin/providers/unregister", "Removes a provider registered with register_openai or register_anthropic.", opAdmProvidersUnregister),
	}
}

// AdminProviderNameParams carry a provider name.
type AdminProviderNameParams struct {
	Name string `json:"name"`
}

// AdminProvidersListResult is the result of admin/providers/list.
type AdminProvidersListResult struct {
	Providers []admin.ProviderInfo `json:"providers"`
}

func opAdmProvidersList(ctx context.Context, s *Server, _ Empty) (AdminProvidersListResult, error) {
	list, err := s.Admin().Providers.List(ctx)
	if err != nil {
		return AdminProvidersListResult{}, err
	}
	if list == nil {
		list = []admin.ProviderInfo{}
	}
	return AdminProvidersListResult{Providers: list}, nil
}

// AdminProvidersModelsParams are the params of admin/providers/models.
type AdminProvidersModelsParams struct {
	// Provider restricts to one provider; empty = every active provider.
	Provider string `json:"provider,omitempty"`
}

// AdminProvidersModelsResult is the result of admin/providers/models.
type AdminProvidersModelsResult struct {
	Models []types.ModelListing `json:"models"`
}

func opAdmProvidersModels(ctx context.Context, s *Server, p AdminProvidersModelsParams) (AdminProvidersModelsResult, error) {
	list, err := s.Admin().Providers.Models(ctx, p.Provider)
	if err != nil {
		return AdminProvidersModelsResult{}, err
	}
	if list == nil {
		list = []types.ModelListing{}
	}
	return AdminProvidersModelsResult{Models: list}, nil
}

// AdminProvidersModelParams are the params of admin/providers/model.
type AdminProvidersModelParams struct {
	Provider string `json:"provider"`
	// ID is the provider-local model id (without the "provider/" prefix).
	ID string `json:"id"`
}

func opAdmProvidersModel(ctx context.Context, s *Server, p AdminProvidersModelParams) (types.ModelListing, error) {
	return s.Admin().Providers.Model(ctx, p.Provider, p.ID)
}

// AdminProvidersConnectParams are the params of admin/providers/connect: the
// provider's name and the credentials (the type comes from the provider).
type AdminProvidersConnectParams struct {
	Name string `json:"name"`
	admin.ConnectCredentials
}

// AdminProvidersConnectResult is the result of admin/providers/connect.
type AdminProvidersConnectResult struct {
	// Models is how many models the provider offers once connected.
	Models int `json:"models"`
}

func opAdmProvidersConnect(ctx context.Context, s *Server, p AdminProvidersConnectParams) (AdminProvidersConnectResult, error) {
	n, err := s.Admin().Providers.Connect(ctx, p.Name, p.ConnectCredentials)
	if err != nil {
		return AdminProvidersConnectResult{}, err
	}
	return AdminProvidersConnectResult{Models: n}, nil
}

func opAdmProvidersDisconnect(_ context.Context, s *Server, p AdminProviderNameParams) (Empty, error) {
	return Empty{}, s.Admin().Providers.Disconnect(p.Name)
}

// AdminProvidersQuotaParams are the params of admin/providers/quota.
type AdminProvidersQuotaParams struct {
	Name string `json:"name"`
	// Refresh asks the provider again instead of serving the cache.
	Refresh bool `json:"refresh,omitempty"`
}

// AdminProvidersQuotaResult is the result of admin/providers/quota.
type AdminProvidersQuotaResult struct {
	Quota *types.Quota `json:"quota"`
	// RetryAfterSeconds is how long the provider asked to wait before polling
	// again (rounded up; 0 = none). When the call fails it travels in the
	// error's data as `retry_after_seconds`.
	RetryAfterSeconds int `json:"retry_after_seconds"`
}

// admRetrySeconds is a Retry-After in whole seconds, rounded UP (the HTTP
// handler's Retry-After header): rounding down would invite a retry too soon.
func admRetrySeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

func opAdmProvidersQuota(ctx context.Context, s *Server, p AdminProvidersQuotaParams) (AdminProvidersQuotaResult, error) {
	q, retry, err := s.Admin().Providers.Quota(ctx, p.Name, p.Refresh)
	if err != nil {
		je := rpcError(err)
		if secs := admRetrySeconds(retry); secs > 0 {
			data, _ := je.Data.(map[string]any)
			je.Data = map[string]any{"kind": data["kind"], "retry_after_seconds": secs}
		}
		return AdminProvidersQuotaResult{}, je
	}
	return AdminProvidersQuotaResult{Quota: q, RetryAfterSeconds: admRetrySeconds(retry)}, nil
}

// AdminProvidersOAuthStartParams are the params of admin/providers/oauth_start.
type AdminProvidersOAuthStartParams struct {
	Provider string `json:"provider"`
}

// AdminProvidersOAuthStartResult is the result of admin/providers/oauth_start
// (the HTTP API's names).
type AdminProvidersOAuthStartResult struct {
	AuthURL      string `json:"auth_url"`
	VerifierCode string `json:"verifier_code"`
}

func opAdmProvidersOAuthStart(_ context.Context, s *Server, p AdminProvidersOAuthStartParams) (AdminProvidersOAuthStartResult, error) {
	u, v, err := s.Admin().Providers.OAuthStart(p.Provider)
	if err != nil {
		return AdminProvidersOAuthStartResult{}, err
	}
	return AdminProvidersOAuthStartResult{AuthURL: u, VerifierCode: v}, nil
}

// AdminProvidersOAuthExchangeParams are the params of
// admin/providers/oauth_exchange (the HTTP API's names).
type AdminProvidersOAuthExchangeParams struct {
	Provider     string `json:"provider"`
	ExchangeCode string `json:"exchange_code"`
	VerifierCode string `json:"verifier_code"`
}

func opAdmProvidersOAuthExchange(_ context.Context, s *Server, p AdminProvidersOAuthExchangeParams) (types.Credentials, error) {
	creds, err := s.Admin().Providers.OAuthExchange(p.Provider, p.ExchangeCode, p.VerifierCode)
	if err != nil {
		return types.Credentials{}, err
	}
	return *creds, nil
}

// AdminProvidersCredentialTypeResult is the result of
// admin/providers/credential_type.
type AdminProvidersCredentialTypeResult struct {
	CredentialType types.CredentialType `json:"credential_type"`
}

func opAdmProvidersCredentialType(_ context.Context, s *Server, p AdminProviderNameParams) (AdminProvidersCredentialTypeResult, error) {
	ct, err := s.Admin().Providers.CredentialType(p.Name)
	if err != nil {
		return AdminProvidersCredentialTypeResult{}, err
	}
	return AdminProvidersCredentialTypeResult{CredentialType: ct}, nil
}

// AdminProvidersListCustomResult is the result of admin/providers/list_custom.
type AdminProvidersListCustomResult struct {
	Providers map[string]types.CustomProvider `json:"providers"`
}

func opAdmProvidersListCustom(_ context.Context, s *Server, _ Empty) (AdminProvidersListCustomResult, error) {
	m := s.Admin().Providers.ListCustom()
	if m == nil {
		m = map[string]types.CustomProvider{}
	}
	return AdminProvidersListCustomResult{Providers: m}, nil
}

// AdminProvidersPutCustomParams are the params of admin/providers/put_custom.
type AdminProvidersPutCustomParams struct {
	Name     string               `json:"name"`
	Provider types.CustomProvider `json:"provider"`
}

func opAdmProvidersPutCustom(_ context.Context, s *Server, p AdminProvidersPutCustomParams) (Empty, error) {
	return Empty{}, s.Admin().Providers.PutCustom(p.Name, p.Provider)
}

func opAdmProvidersDeleteCustom(_ context.Context, s *Server, p AdminProviderNameParams) (Empty, error) {
	return Empty{}, s.Admin().Providers.DeleteCustom(p.Name)
}

// AdminProvidersReloadCustomResult is the result of
// admin/providers/reload_custom.
type AdminProvidersReloadCustomResult struct {
	// Status is "reloaded" or "reloaded_with_conflicts".
	Status string `json:"status"`
	// Message lists the entries skipped because a programmatic provider
	// already uses their name (only with reloaded_with_conflicts).
	Message string `json:"message,omitempty"`
}

func opAdmProvidersReloadCustom(_ context.Context, s *Server, _ Empty) (AdminProvidersReloadCustomResult, error) {
	err := s.Admin().Providers.ReloadCustom()
	switch {
	case err == nil:
		return AdminProvidersReloadCustomResult{Status: "reloaded"}, nil
	case errors.Is(err, admin.ErrProviderNameConflict):
		return AdminProvidersReloadCustomResult{Status: "reloaded_with_conflicts", Message: err.Error()}, nil
	}
	return AdminProvidersReloadCustomResult{}, err
}

// AdminProvidersRegisterOpenAIParams are the params of
// admin/providers/register_openai.
type AdminProvidersRegisterOpenAIParams struct {
	Name string `json:"name"`
	// URL is the Chat Completions base URL (including any /v1).
	URL     string `json:"url"`
	Display string `json:"display,omitempty"`
	// Headers are sent on every request: this is where ANY authentication goes.
	Headers map[string]string `json:"headers,omitempty"`
	// ReasoningSplit sends "reasoning_split": true (MiniMax-compatible backends).
	ReasoningSplit bool `json:"reasoning_split,omitempty"`
}

func opAdmProvidersRegisterOpenAI(_ context.Context, s *Server, p AdminProvidersRegisterOpenAIParams) (Empty, error) {
	opts := []admin.CustomProviderOption{admin.ProviderWithDisplay(p.Display), admin.ProviderWithHeaders(p.Headers)}
	if p.ReasoningSplit {
		opts = append(opts, admin.ProviderWithReasoningSplit())
	}
	return Empty{}, admRegisterErr(s.Admin().Providers.RegisterOpenAI(p.Name, p.URL, opts...))
}

// admRegisterErr gives the registration's plain errors (a reserved name, a name
// already registered: admin returns them without a sentinel) their kind: a
// duplicate is a conflict, anything else the caller's input.
func admRegisterErr(err error) error {
	if err == nil || admin.Kind(err) != "" {
		return err
	}
	if strings.Contains(err.Error(), "already registered") {
		return rpcErr("conflict", "%s", err.Error())
	}
	return errInvalid("%s", err.Error())
}

// AdminProvidersRegisterAnthropicParams are the params of
// admin/providers/register_anthropic.
type AdminProvidersRegisterAnthropicParams struct {
	Name string `json:"name"`
	// URL is the API root WITHOUT /v1.
	URL     string            `json:"url"`
	Display string            `json:"display,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func opAdmProvidersRegisterAnthropic(_ context.Context, s *Server, p AdminProvidersRegisterAnthropicParams) (Empty, error) {
	return Empty{}, admRegisterErr(s.Admin().Providers.RegisterAnthropic(p.Name, p.URL,
		admin.ProviderWithDisplay(p.Display), admin.ProviderWithHeaders(p.Headers)))
}

func opAdmProvidersUnregister(_ context.Context, s *Server, p AdminProviderNameParams) (Empty, error) {
	return Empty{}, s.Admin().Providers.Unregister(p.Name)
}
