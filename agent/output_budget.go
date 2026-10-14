package agent

import "github.com/gurcuff91/harness/types"

// ── Output budget (max_tokens) ────────────────────────────────────────────
//
// Providers that count input + max_tokens against ONE window (MiniMax,
// OpenAI-compatible gateways) refuse a request outright once the reserved
// output no longer fits: observed live, MiniMax-M3 with a catalog-supplied
// max_tokens of 512k started failing with "context window exceeds limit" as
// soon as the input passed ~half of its ~1M window. So:
//
//  1. The session CEILING (sessionMaxTokens) is the user's explicit
//     AgentOptions.MaxTokens when set — it always wins, whatever the catalog
//     says — else the catalog's per-model value capped at
//     defaultMaxOutputTokens (a coding agent never needs a 512k reply), else
//     fallbackMaxOutputTokens.
//  2. Each REQUEST is then bounded by the room left in the window
//     (requestMaxTokens): window − estimated input − safety margin, floored at
//     minOutputTokens so a nearly-full session still gets a useful answer
//     (and compacts first — see needsCompaction).
const (
	// defaultMaxOutputTokens caps the catalog's per-model max output when
	// the user didn't configure AgentOptions.MaxTokens.
	defaultMaxOutputTokens = 64000
	// fallbackMaxOutputTokens is used when nothing knows the model's limit.
	fallbackMaxOutputTokens = 32000
	// minOutputTokens is the floor the window-based clamp never goes below —
	// unless the user's own ceiling is lower (their choice wins).
	minOutputTokens = 16000
	// contextSafetyMargin reserves this fraction of the window: providers
	// tokenize differently from our estimates.
	contextSafetyMargin = 0.05
)

// sessionMaxTokens resolves a session's output ceiling. explicit is
// AgentOptions.MaxTokens (0 = not configured); meta is the model's catalog
// metadata (may be nil).
func sessionMaxTokens(explicit int, meta *types.ModelMeta) int {
	if explicit > 0 {
		return explicit // the user's choice — never overridden by catalog or default cap
	}
	if meta != nil && meta.MaxTokens > 0 {
		return min(meta.MaxTokens, defaultMaxOutputTokens)
	}
	return fallbackMaxOutputTokens
}

// requestMaxTokens bounds ceiling by the room left in the window for a
// request whose input is estimated at inputTokens. Never above ceiling;
// never below min(ceiling, minOutputTokens).
func requestMaxTokens(ceiling, window, inputTokens int) int {
	if window <= 0 {
		return ceiling
	}
	margin := int(float64(window) * contextSafetyMargin)
	room := window - inputTokens - margin
	return max(min(ceiling, room), outputFloor(ceiling, window))
}

// outputFloor is the least output the window clamp ever leaves: the user's
// ceiling if lower (their choice), else minOutputTokens — but never more
// than a quarter of the window (small-window models, where a fixed 16k
// reserve would not even fit).
func outputFloor(ceiling, window int) int {
	f := min(ceiling, minOutputTokens)
	if window > 0 {
		f = min(f, window/4)
	}
	return f
}

// needsCompaction reports whether a request with this estimated input would
// leave no room for a useful answer: input plus the minimum output reserve
// crosses autoCompactThreshold of the window. Reserved output counts — a
// provider that sums input + max_tokens refuses long before input alone
// reaches the window.
func needsCompaction(window, inputTokens, ceiling int) bool {
	if window <= 0 {
		return false
	}
	return float64(inputTokens+outputFloor(ceiling, window)) >= autoCompactThreshold*float64(window)
}

// estimateTokens is a deliberately GENEROUS chars→tokens estimate for content
// the provider hasn't counted yet (messages appended since the last real
// usage report): over-estimating only costs a slightly smaller max_tokens or
// an earlier compaction; under-estimating costs a refused request.
func estimateTokens(chars int) int {
	return int(float64(chars) / compactionCharsPerToken)
}

// estimateInputTokens estimates a request's input size as the larger of two
// figures, each covering the other's blind spot:
//   - lastReal: the provider's own count for the previous request (exact,
//     but stale — it misses whatever was appended since: tool results, the
//     model's last reply);
//   - a generous chars-based estimate of the request about to be sent
//     (current, but blind to provider tokenization quirks).
func estimateInputTokens(lastReal int, req *types.Request) int {
	chars := len(req.SystemPrompt) + approxSize(req.Messages)
	for _, t := range req.Tools {
		chars += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	return max(lastReal, estimateTokens(chars))
}
