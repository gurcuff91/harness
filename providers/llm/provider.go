package llm

import (
	"context"

	"github.com/gurcuff91/harness/types"
)

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
	FetchModels() []types.ModelMeta
	// ModelMeta returns capability and pricing metadata for a specific model ID.
	// Checks the provider's cache first; falls back to the registry and name inference.
	// Returns nil if nothing is known about the model.
	ModelMeta(modelID string) *types.ModelMeta
}
