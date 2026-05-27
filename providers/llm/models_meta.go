package llm

// ModelMeta holds all known information about a model:
// capabilities (from provider APIs or llm-registry) and
// pricing (always from llm-registry).
type ModelMeta struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`

	// Capabilities
	ContextWindow  int  `json:"context_window"`
	MaxTokens      int  `json:"max_tokens"`
	Vision         bool `json:"vision"`
	Thinking       bool `json:"thinking"`
	IsSubscription bool `json:"is_subscription"` // flat sub or local compute

	// Pricing ($ per 1M tokens) — sourced from llm-registry
	InputCost      float64 `json:"input_cost,omitempty"`
	OutputCost     float64 `json:"output_cost,omitempty"`
	CacheReadCost  float64 `json:"cache_read_cost,omitempty"`
	CacheWriteCost float64 `json:"cache_write_cost,omitempty"`
}

// ModelInfo is a lightweight reference used for listing available models.
type ModelInfo struct {
	Name     string
	Provider string
	Active   bool
}
