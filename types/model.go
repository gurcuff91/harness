package types

// ── Model types ──────────────────────────────────────────────────────────

// ModelMeta holds capabilities and pricing metadata for an LLM model.
type ModelMeta struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`

	// Context
	ContextWindow int `json:"context_window"`
	MaxTokens     int `json:"max_tokens"`

	// Capabilities
	Vision           bool `json:"vision"`
	Thinking         bool `json:"thinking"`
	ThinkingAdaptive bool `json:"thinking_adaptive,omitempty"`
	ThinkingLegacy   bool `json:"thinking_legacy,omitempty"`
	// EffortLevels reports which of harness's universal thinking levels
	// (currently "low"|"medium"|"high"|"xhigh"|"max") this SPECIFIC model
	// supports, keyed by level name — e.g. {"low":true,...,"xhigh":false,
	// "max":true}. Populated only by providers whose API exposes real,
	// authoritative per-model capability data (confirmed live: Anthropic's
	// GET /v1/models returns capabilities.effort.<level>.supported for
	// every model) — a level absent from this map, or a nil/empty map
	// entirely, means "unknown/not reported", NOT "unsupported"; callers
	// must fall back to their own static knowledge in that case (e.g.
	// Codex OAuth, which has no such endpoint and hardcodes its own
	// clamp). This is the generic, provider-agnostic home for ANY
	// provider that can report this — not Anthropic-specific.
	EffortLevels map[string]bool `json:"effort_levels,omitempty"`

	// Pricing (per million tokens, USD)
	InputPrice  float64 `json:"input_price"`
	OutputPrice float64 `json:"output_price"`
	CacheRead   float64 `json:"cache_read"`
	CacheWrite  float64 `json:"cache_write"`

	// Subscription — true if billed as a flat fee (e.g. Claude Max, OpenCode Go)
	IsSubscription bool `json:"is_subscription"`

	// Sources says where each datum above came from, keyed by field group:
	// "display_name", "context_window", "max_tokens", "vision", "thinking" and
	// "pricing". Values (SourceProvider … SourceUnknown) tell a client how far to
	// trust a number — a provider-reported context window is authoritative, a
	// name-inferred or default one is a guess — and disambiguate what a zero
	// means: pricing from the catalog with 0 prices is a free model, pricing
	// "unknown" with 0 prices is no data at all. Absent (nil) on metadata that
	// was built without provenance (e.g. a custom provider's FetchModels hook).
	Sources map[string]string `json:"sources,omitempty"`
}

// Provenance values for ModelMeta.Sources, strongest to weakest.
const (
	// SourceProvider — reported by the provider's own API (authoritative).
	SourceProvider = "provider"
	// SourceCatalog — taken from the OpenRouter catalog (a fallback; it may
	// describe a different deployment of the same model).
	SourceCatalog = "catalog"
	// SourceRegistry — harness's small built-in table of models the catalog
	// doesn't carry under a matching id.
	SourceRegistry = "registry"
	// SourceInferred — guessed from the model's name.
	SourceInferred = "inferred"
	// SourceDefault — no source knew; a generic default (128k context, 32k
	// output, false capability, display name = id).
	SourceDefault = "default"
	// SourceUnknown — pricing only: no source had prices, so the 0s mean
	// "unknown", not "free".
	SourceUnknown = "unknown"
)

// Source field-group keys of ModelMeta.Sources.
const (
	FieldDisplayName   = "display_name"
	FieldContextWindow = "context_window"
	FieldMaxTokens     = "max_tokens"
	FieldVision        = "vision"
	FieldThinking      = "thinking"
	FieldPricing       = "pricing"
)

// ModelInfo is a lightweight reference used for listing available models.
type ModelInfo struct {
	ID          string
	DisplayName string
	Provider    string
	Active      bool
}
