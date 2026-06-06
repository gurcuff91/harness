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
