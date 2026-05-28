package types

// SessionStats is a snapshot of accumulated token usage and cost for a session.
type SessionStats struct {
	// Accumulated across all turns
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int

	// Derived
	CostUSD       float64 // total cost in USD (always calculated from model pricing)
	ContextUsage    float64 // last turn input tokens / model context window (0.0–1.0)
	ContextWindow int     // model context window size (tokens)
}
