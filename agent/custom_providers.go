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
	fetchModels func() ([]types.ModelMeta, error)
}

// CustomProviderOption configures a NewOpenAIProvider call.
type CustomProviderOption func(*customProviderConfig)

// ProviderWithDisplay sets the human-friendly display name shown in
// listings (e.g. GET /api/providers, `harness providers`). Defaults to
// the provider's own name when omitted.
func ProviderWithDisplay(display string) CustomProviderOption {
	return func(c *customProviderConfig) { c.display = display }
}

// ProviderWithHeaders sets the HTTP headers sent on every request this
// provider makes — both chat completions and model listing. This is where
// ANY authentication goes: there is no separate API key concept for a
// custom provider (see NewOpenAIProvider's doc comment for why) — set
// whatever the endpoint needs here, e.g.
// map[string]string{"Authorization": "Bearer " + token} or
// map[string]string{"X-Api-Key": key}.
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
// etc. fn takes no arguments — if it needs credentials, capture them in
// its own closure (same as ProviderWithHeaders, there's no separate apiKey
// threaded through).
func ProviderWithFetchModels(fn func() ([]types.ModelMeta, error)) CustomProviderOption {
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
// discovery).
//
// There is no apiKey parameter — authentication is ALWAYS carried in
// ProviderWithHeaders, symmetric with the declarative settings.json path
// (whose Headers field is likewise the only place authentication lives).
// Neither path has a separate "API key" concept: there is no reliable way
// to guess, for an arbitrary custom backend, whether it expects
// "Authorization: Bearer <token>", a custom header ("X-Api-Key: ..."), or
// several headers combined. `harness connect <name> <key>` does NOT apply
// to a custom provider (rejected, same as it already is for auto-detected
// Ollama) — the provider is always active once registered.
//
//	err := agent.NewOpenAIProvider("my-proxy", "https://my-proxy.internal/v1",
//		agent.ProviderWithDisplay("Acme Internal Proxy"),
//		agent.ProviderWithHeaders(map[string]string{"X-Api-Key": apiKey}),
//	)
func NewOpenAIProvider(name, url string, opts ...CustomProviderOption) error {
	var cfg customProviderConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return providers.RegisterOpenAI(name, url, cfg.display, cfg.headers, cfg.fetchModels)
}
