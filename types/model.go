package types

// ── Model types ──────────────────────────────────────────────────────────

// ModelMeta holds capabilities and pricing metadata for an LLM model.
type ModelMeta struct {
	ID          string
	DisplayName string

	// Context
	ContextWindow int
	MaxTokens     int

	// Capabilities
	Vision           bool
	Thinking         bool // supports any thinking
	ThinkingAdaptive bool // supports adaptive thinking (output_config effort)
	ThinkingLegacy   bool // supports legacy thinking (budget_tokens)

	// Pricing (per million tokens, USD)
	InputPrice  float64
	OutputPrice float64
	CacheRead   float64
	CacheWrite  float64

	// Subscription — true if billed as a flat fee (e.g. Claude Max, OpenCode Go)
	IsSubscription bool
}

// ModelInfo is a lightweight reference used for listing available models.
type ModelInfo struct {
	ID          string
	DisplayName string
	Provider    string
	Active      bool
}
