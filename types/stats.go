package types

// SessionStats is a snapshot of accumulated token usage and cost for a session.
// Use this for programmatic access, billing tracking, and post-session analysis.
type SessionStats struct {
	// Accumulated across all turns — for billing analysis
	// Note: InputTokens grows exponentially (each turn includes full history)
	// Use CostUSD for actual spend tracking.
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CacheRead    int     `json:"cache_read"`
	CacheWrite   int     `json:"cache_write"`

	// Derived — calculated by the session
	CostUSD       float64 `json:"cost_usd"`
	ContextUsage  float64 `json:"context_usage"`
	ContextWindow int     `json:"context_window"`
}
