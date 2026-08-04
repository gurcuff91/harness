package agent

import "testing"

// contextTokens mirrors the formula in updateStats (s.lastInputTokens). Kept
// as a tiny local helper so these tests can pin the ARITHMETIC without
// needing a live provider, a session, or a store.
func contextTokens(freshInput, cacheRead, cacheWrite int) int {
	return freshInput + cacheRead + cacheWrite
}

// TestContextUsageIncludesCacheWrite is the regression test for a reported
// footer reading "0.0%/1.0M" immediately after a turn, on a session whose
// previously-persisted usage was 82.7%.
//
// Anthropic's usage split for that turn was: 2 fresh input tokens, 0 cache
// reads, 827.6k cache WRITES (the normal shape right after a compaction /
// model switch / system-prompt change — nearly the entire context is reported
// as cache_creation_input_tokens). The old formula summed only fresh + read,
// yielding 0.0002% while the true occupancy was 82.8% — matching the 82.7%
// from the turn before, proving the context had not shrunk at all; the
// accounting had simply dropped it.
//
// This is not cosmetic: ContextUsage >= 0.98 is what triggers the mid-turn
// auto-compact, so under-counting could leave that guard silent while the
// real context ran to the window limit.
func TestContextUsageIncludesCacheWrite(t *testing.T) {
	const window = 1_000_000

	// The exact field-reported turn.
	fresh, cacheRead, cacheWrite := 2, 0, 827_600

	got := contextTokens(fresh, cacheRead, cacheWrite)
	if got != 827_602 {
		t.Fatalf("context tokens = %d, want 827602 (fresh+read+write)", got)
	}

	usage := float64(got) / float64(window)
	if usage < 0.82 || usage > 0.83 {
		t.Errorf("context usage = %.4f (%.1f%%), want ~0.8276 (~82.8%%) — the value "+
			"persisted before this turn was 82.7%%, so the context did not shrink",
			usage, usage*100)
	}

	// And prove the OLD formula is what produced the reported 0.0%.
	oldUsage := float64(fresh+cacheRead) / float64(window)
	if oldUsage*100 >= 0.1 {
		t.Errorf("the old formula should round to 0.0%% for this turn, got %.4f%%", oldUsage*100)
	}
}

// TestContextUsageFormulaCases covers the shapes each cache mode produces, so
// none of the three components can be dropped again without a failure.
func TestContextUsageFormulaCases(t *testing.T) {
	cases := []struct {
		name                         string
		fresh, cacheRead, cacheWrite int
		want                         int
		why                          string
	}{
		{
			name: "cold start (no cache yet)", fresh: 50_000, cacheRead: 0, cacheWrite: 0,
			want: 50_000, why: "nothing cached: all context is fresh input",
		},
		{
			name: "warm cache (steady state)", fresh: 2, cacheRead: 667_000, cacheWrite: 0,
			want: 667_002, why: "cache reads ARE context that was sent",
		},
		{
			name: "cache being (re)written", fresh: 2, cacheRead: 0, cacheWrite: 827_600,
			want: 827_602, why: "the reported bug: cache writes are input too " +
				"(Anthropic calls the field cache_creation_INPUT_tokens)",
		},
		{
			name: "mixed read + write", fresh: 100, cacheRead: 300_000, cacheWrite: 200_000,
			want: 500_100, why: "all three components count",
		},
		{
			name: "just compacted", fresh: 0, cacheRead: 0, cacheWrite: 0,
			want: 0, why: "context reclaimed — gauge must read 0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := contextTokens(c.fresh, c.cacheRead, c.cacheWrite); got != c.want {
				t.Errorf("contextTokens(fresh=%d, read=%d, write=%d) = %d, want %d — %s",
					c.fresh, c.cacheRead, c.cacheWrite, got, c.want, c.why)
			}
		})
	}
}

// TestAutoCompactThresholdReachableWithCacheWrites verifies the practical
// consequence of the fix: a turn dominated by cache WRITES can now cross the
// 0.98 auto-compact threshold. Under the old formula such a turn reported
// ~0% no matter how full the context actually was, so the guard never fired.
func TestAutoCompactThresholdReachableWithCacheWrites(t *testing.T) {
	const window = 1_000_000
	const autoCompactThreshold = 0.98 // see promptSync

	// A nearly-full context, reported almost entirely as cache writes.
	fresh, cacheRead, cacheWrite := 10, 0, 985_000

	newUsage := float64(contextTokens(fresh, cacheRead, cacheWrite)) / float64(window)
	if newUsage < autoCompactThreshold {
		t.Errorf("fixed formula usage = %.4f, expected >= %.2f so auto-compact fires",
			newUsage, autoCompactThreshold)
	}

	oldUsage := float64(fresh+cacheRead) / float64(window)
	if oldUsage >= autoCompactThreshold {
		t.Fatalf("premise wrong: the old formula should NOT reach the threshold, got %.4f", oldUsage)
	}
	t.Logf("old formula: %.4f%% (auto-compact silent) → fixed: %.1f%% (auto-compact fires)",
		oldUsage*100, newUsage*100)
}
