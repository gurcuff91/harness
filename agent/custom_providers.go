package agent

import (
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// customProviderConfig is the option accumulator CustomProviderOption
// functions mutate. Never exposed directly — only its construction via
// NewOpenAIProvider's option chain is public. Keeping this unexported
// (rather than exposing internal/providers.CustomOpenAI or its own
// exported struct with public fields) is what lets NewOpenAIProvider stay
// a pure functional-options constructor without ever putting an
// internal/… type in a public signature — see AGENTS.md's "SDK boundary"
// rule.
type customProviderConfig struct {
	display     string
	headers     map[string]string
	fetchModels func(apiKey string) ([]types.ModelMeta, error)
}

// CustomProviderOption configures a NewOpenAIProvider call.
type CustomProviderOption func(*customProviderConfig)

// ProviderWithDisplay sets the human-friendly display name shown in
// listings (e.g. GET /api/providers, `harness providers`). Defaults to
// the provider's own name when omitted.
func ProviderWithDisplay(display string) CustomProviderOption {
	return func(c *customProviderConfig) { c.display = display }
}

// ProviderWithHeaders sets extra HTTP headers sent on every request this
// provider makes — both chat completions and model listing.
func ProviderWithHeaders(headers map[string]string) CustomProviderOption {
	return func(c *customProviderConfig) { c.headers = headers }
}

// ProviderWithFetchModels overrides model discovery entirely. The default
// (used when this option is omitted) is an HTTP GET to <url>/models,
// parsed as {"data": [{"id": "..."}]} with every returned ID accepted
// as-is (no OpenAI-specific name filtering — see
// docs/plans/2026-09-21-custom-providers-design.md for why that matters
// for an arbitrary custom backend). Use this when a deployment needs
// something the default can't do: a static model list, a differently
// shaped listing endpoint, additional client-side capability enrichment,
// etc. fn receives the API key NewOpenAIProvider was given.
func ProviderWithFetchModels(fn func(apiKey string) ([]types.ModelMeta, error)) CustomProviderOption {
	return func(c *customProviderConfig) { c.fetchModels = fn }
}

// NewOpenAIProvider registers a custom OpenAI Chat Completions-compatible
// provider globally for this PROCESS — usable by any Agent built
// afterward via "name/model", exactly like a built-in provider (see
// Agent.Providers()/Agent.Models()). name must not collide with a
// built-in provider's own name (anthropic, openai, minimax, …).
//
// This is the fully PROGRAMMATIC registration path — call it once, early,
// typically in main() before constructing any Agent (registration is not
// safe to race against concurrent reads; see internal/providers/
// registry.go's registryMu doc comment for the exact contract). It is
// deliberately distinct from the DECLARATIVE settings.json path
// (`harness provider add`, backed by types.CustomProvider): there's no
// Disabled flag here (simply don't call this to leave a provider out) and
// no ModelsURL (use ProviderWithFetchModels for non-default model
// discovery) — apiKey is used directly and held only in memory, never
// written to credentials.json, the same way a built-in api-key provider
// behaves when activated via its environment variable instead of
// `harness connect`.
//
//	err := agent.NewOpenAIProvider("my-proxy", "https://my-proxy.internal/v1", apiKey,
//		agent.ProviderWithDisplay("Acme Internal Proxy"),
//		agent.ProviderWithHeaders(map[string]string{"X-Org-Id": "acme"}),
//	)
func NewOpenAIProvider(name, url, apiKey string, opts ...CustomProviderOption) error {
	var cfg customProviderConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return providers.RegisterOpenAI(name, url, apiKey, cfg.display, cfg.headers, cfg.fetchModels)
}
