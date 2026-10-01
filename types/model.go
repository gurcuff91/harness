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
}

// ModelInfo is a lightweight reference used for listing available models.
type ModelInfo struct {
	ID          string
	DisplayName string
	Provider    string
	Active      bool
}
