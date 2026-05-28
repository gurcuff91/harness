package types

// SessionStats is a snapshot of accumulated token usage and cost for a session.
// Use this for programmatic access, billing tracking, and post-session analysis.
type SessionStats struct {
	// Accumulated across all turns — for billing analysis
	// Note: InputTokens grows exponentially (each turn includes full history)
	// Use CostUSD for actual spend tracking.
	InputTokens  int // sum of input tokens across all turns (billing reference)
	OutputTokens int // sum of output tokens across all turns
	CacheRead    int // sum of cache read tokens across all turns
	CacheWrite   int // sum of cache write tokens across all turns

	// Derived — calculated by the session
	CostUSD       float64 // accumulated USD cost (always calculated from model pricing)
	ContextUsage  float64 // last turn input / context window (0.0–1.0)
	ContextWindow int     // model context window size (tokens)
}
