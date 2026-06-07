package providers

import (
	"context"

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
	// IsActive returns true if this provider has valid credentials and is reachable.
	IsActive() bool
	// Models returns the cached model list for this provider.
	// Fast, no API call — populated by FetchModels().
	Models() []types.ModelMeta
	// FetchModels refreshes the internal model cache from the provider API.
	// Each model is fully enriched: capabilities from the provider API
	// and pricing from llm-registry.
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
