package tui

import (
	"context"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// TestFooterTotalsSurviveTheFirstEventAfterResume is the regression test for
// the reported footer behavior:
//
//	on resume:      ↑112.6M ↓2.5M R2141.0M W62.2M $1099.055 82.7%/1.0M
//	after 1st turn: ↑2      ↓2.5M R0       W827.6k $1101.125  0.0%/1.0M
//
// Three of the six stats collapsed. Cause: loadStatsFromSession loaded the
// persisted SESSION TOTALS, then the first "tokens" event overwrote
// input/cacheRead/cacheWrite with that turn's PER-TURN values — two
// semantics fighting over the same fields. (↓ and $ were spared only because
// they already read accumulated fields.)
//
// The footer's ↑ ↓ R W $ are session totals; only the %/window gauge is
// live-context. This drives the event loop with a realistic resume-then-turn
// sequence and asserts the totals only ever grow.
func TestFooterTotalsSurviveTheFirstEventAfterResume(t *testing.T) {
	tui := newTestTUIForEvents()

	// 1. Resume: totals loaded from the persisted SessionStats.
	tui.loadStatsFromSession(&client.Session{
		SessionMeta: store.SessionMeta{Stats: types.SessionStats{
			InputTokens:   112_626_989,
			OutputTokens:  2_529_943,
			CacheRead:     2_216_313_730,
			CacheWrite:    64_706_276,
			CostUSD:       1099.055,
			ContextUsage:  0.827,
			ContextWindow: 1_000_000,
		}},
	})

	if tui.stats.input != 112_626_989 {
		t.Fatalf("after resume: input = %d, want the persisted total 112626989", tui.stats.input)
	}

	// 2. First turn's tokens event. The agent now sends session totals in the
	//    accumulated fields (slightly higher — this turn added to them) and
	//    the turn's own context in the live-context fields.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan client.Event, 4)
	events <- client.Event{
		Type: "tokens",
		// live context for this turn
		Input:         827_602,
		ContextUsage:  0.8276,
		ContextWindow: 1_000_000,
		// session totals, now including this turn
		TotalInput:  112_627_100,
		TotalOutput: 2_530_500,
		CacheRead:   2_216_500_000,
		CacheWrite:  65_533_876,
		CostUSD:     1101.125,
	}
	close(events)
	tui.consumeEvents(ctx, events)

	// The totals must have GROWN, never collapsed to per-turn values.
	checks := []struct {
		name       string
		got, floor int
	}{
		{"input (↑)", tui.stats.input, 112_626_989},
		{"output (↓)", tui.stats.output, 2_529_943},
		{"cacheRead (R)", tui.stats.cacheRead, 2_216_313_730},
		{"cacheWrite (W)", tui.stats.cacheWrite, 64_706_276},
	}
	for _, c := range checks {
		if c.got < c.floor {
			t.Errorf("%s collapsed after the first event: got %d, must be >= the resumed total %d",
				c.name, c.got, c.floor)
		}
	}
	if tui.stats.cost < 1099.055 {
		t.Errorf("cost ($) went backwards: got %v, want >= 1099.055", tui.stats.cost)
	}

	// The gauge, by contrast, IS live context and tracks the current turn.
	if tui.stats.contextPct < 0.82 || tui.stats.contextPct > 0.83 {
		t.Errorf("context gauge = %.4f, want ~0.8276 (live context for this turn)", tui.stats.contextPct)
	}
}

// TestFooterGaugeDropsToZeroOnCompaction verifies the ONE stat that should
// shrink does shrink: after a compaction the agent emits a tokens event with
// the live context zeroed (and history intact), so the gauge reads 0% right
// away instead of showing the pre-compaction figure until the next turn.
func TestFooterGaugeDropsToZeroOnCompaction(t *testing.T) {
	tui := newTestTUIForEvents()
	tui.loadStatsFromSession(&client.Session{
		SessionMeta: store.SessionMeta{Stats: types.SessionStats{
			InputTokens:   5_000,
			OutputTokens:  1_000,
			CostUSD:       1.5,
			ContextUsage:  0.98,
			ContextWindow: 1_000_000,
		}},
	})
	if tui.stats.contextPct != 0.98 {
		t.Fatalf("setup: gauge = %v, want 0.98", tui.stats.contextPct)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan client.Event, 2)
	events <- client.Event{Type: "compact_end", Summary: "summary"}
	events <- client.Event{ // what compact() now emits right after
		Type:          "tokens",
		Input:         0,
		ContextUsage:  0,
		ContextWindow: 1_000_000,
		TotalInput:    5_000,
		TotalOutput:   1_000,
		CostUSD:       1.5,
	}
	close(events)
	tui.consumeEvents(ctx, events)

	if tui.stats.contextPct != 0 {
		t.Errorf("gauge = %v after compaction, want 0 — the context was reclaimed", tui.stats.contextPct)
	}
	// History must NOT be reset by a compaction.
	if tui.stats.input != 5_000 || tui.stats.output != 1_000 {
		t.Errorf("compaction wiped session history: input=%d output=%d, want 5000/1000",
			tui.stats.input, tui.stats.output)
	}
	if tui.stats.cost != 1.5 {
		t.Errorf("compaction wiped accumulated cost: got %v, want 1.5", tui.stats.cost)
	}
}
