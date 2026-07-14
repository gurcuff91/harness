package providers

import (
	"context"
	"fmt"
	"os"

	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/types"
)

// Provider is the core interface implemented by all LLM providers.
// It lives in providers/ (not providers/llm/) because it's the contract
// of the providers package, not an implementation detail.

// Provider abstracts LLM API differences.
// All providers implement streaming — there is no non-streaming fallback.
// Messages are provider-agnostic (types.Message) — each provider translates
// to its own wire format internally before making API calls.
type Provider interface {
	// CompleteStream sends a request and streams events via callback.
	// The final types.Response is returned when the stream completes.
	CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error)
	// Name returns the provider slug (e.g. "anthropic", "openai", "ollama").
	Name() string
	// DisplayName returns the human-friendly provider name (e.g. "OpenAI",
	// "Anthropic", "Claude OAuth"). Used for UI rendering in palettes/menus.
	DisplayName() string
	// Description returns a short, dynamic blurb about the provider's current
	// state — e.g. "12 models" when connected, "subscription" for OAuth, or
	// "not connected" when inactive. Computed live, not hardcoded.
	Description() string
	// IsActive returns true if this provider has valid credentials and is reachable.
	IsActive() bool
	// Models returns the cached model list for this provider.
	// Fast, no API call — populated by FetchModels().
	Models() []types.ModelMeta
	// FetchModels refreshes the internal model cache from the provider API.
	// Each model is fully enriched: capabilities from the provider API
	// and pricing from the OpenRouter catalog.
	// Returns error if credentials are invalid or provider is unreachable.
	FetchModels() ([]types.ModelMeta, error)
	// ModelMeta returns capability and pricing metadata for a specific model ID.
	// Checks the provider's cache first; falls back to the registry and name inference.
	// Returns nil if nothing is known about the model.
	ModelMeta(modelID string) *types.ModelMeta

	// CredentialType returns what kind of credentials this provider expects.
	CredentialType() types.CredentialType

	// ResolveCredentials reads from the credential chain.
	ResolveCredentials() (types.Credentials, error)

	// ActivationSource returns how this provider was activated.
	ActivationSource() ActivationSource

	// Connect saves credentials, validates them by fetching models,
	// and activates the provider. On failure, credentials are rolled back.
	Connect(creds types.Credentials) error

	// Disconnect clears all credentials and deactivates the provider.
	Disconnect() error
}

// ActivationSource describes how a provider got its credentials.
type ActivationSource string

const (
	// ActivationNone — provider is not active.
	ActivationNone ActivationSource = "none"
	// ActivationCredentials — activated via credentials.json (manageable by TUI).
	ActivationCredentials ActivationSource = "credentials"
	// ActivationEnvVar — activated via environment variable (de-facto, not manageable).
	ActivationEnvVar ActivationSource = "envvar"
	// ActivationAuto — auto-detected (e.g. ollama running locally, not manageable).
	ActivationAuto ActivationSource = "auto"
)

// describeState builds the dynamic Description shared by all providers, in a
// uniform two-segment format: "<auth type> · <state>".
//
//	auth type — stable nature of the provider: "subscription" (OAuth),
//	            "API key", or "local" (auto-detected, e.g. Ollama).
//	state     — dynamic: "N models" when connected, else "not connected".
//
// Examples:
//
//	subscription · 8 models
//	API key · 12 models
//	local · 2 models
//	API key · not connected
//
// Every provider's Description() delegates here so the wording stays identical
// across the registry.
func describeState(p Provider) string {
	var authType string
	switch p.CredentialType() {
	case types.CredTypeOAuth:
		authType = "subscription"
	case types.CredTypeAPIKey:
		authType = "API key"
	default: // CredTypeNone — auto-detected local providers
		authType = "local"
	}

	state := "not connected"
	if p.IsActive() {
		if n := len(p.Models()); n > 0 {
			state = fmt.Sprintf("%d models", n)
		} else {
			state = "connected"
		}
	}
	return authType + " · " + state
}

// ── API-key credential helpers ────────────────────────────────────────────
// Shared by the api_key providers so the env→credential cascade lives in ONE
// place (the provider only supplies its env-var name).

// resolveAPIKey applies the api-key cascade: environment variable first, then
// the stored credential. Returns the resolved key ("" if none) and the
// ActivationSource that produced it — so a provider's ResolveCredentials and
// ActivationSource share one source of truth.
func resolveAPIKey(provider, envVar string) (string, ActivationSource) {
	if v := os.Getenv(envVar); v != "" {
		return v, ActivationEnvVar
	}
	if v, ok := config.GetCredentialsManager().APIKey(provider); ok {
		return v, ActivationCredentials
	}
	return "", ActivationNone
}

// storeAPIKey persists an API key for a provider as a typed credential.
func storeAPIKey(provider, key string) error {
	return config.GetCredentialsManager().SetCredential(provider,
		config.ProviderCredential{Type: "api_key", APIKey: key})
}

// deleteCredential removes a provider's stored credential.
func deleteCredential(provider string) error {
	return config.GetCredentialsManager().DeleteCredential(provider)
}
