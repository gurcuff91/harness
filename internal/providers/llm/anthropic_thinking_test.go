package llm

import (
	"testing"

	"github.com/gurcuff91/harness/types"
)

// TestBuildAnthropicThinkingFromMeta_AdaptiveOnlyModelDegradesInsteadOfDisabled
// is the regression test for a real bug: models that report
// ThinkingAdaptive=true, ThinkingLegacy=false (confirmed live against
// claude-opus-5-5, claude-opus-5, claude-sonnet-5, claude-fable-5,
// claude-opus-4-8, claude-opus-4-7 — the entire opus-5/"5" generation, per
// Anthropic's own reported model capabilities) have dropped legacy
// thinking.type "disabled"/"enabled" support entirely. A request with
// level=="off" or level=="" (the exact shape generateCompactionSummary's
// own *types.Request sends — it never sets ThinkingLevel at all) used to
// unconditionally send {"type":"disabled"}, which these models reject with
// a 400: "\"thinking.type.disabled\" is not supported for this model. Use
// \"thinking.type.adaptive\" and \"output_config.effort\" to control
// thinking behavior." Confirmed live: {"type":"adaptive","effort":"low"} is
// accepted and is the closest available equivalent to "off" these models
// allow — this is exactly what the fix degrades to instead of erroring.
func TestBuildAnthropicThinkingFromMeta_AdaptiveOnlyModelDegradesInsteadOfDisabled(t *testing.T) {
	meta := &types.ModelMeta{ThinkingAdaptive: true, ThinkingLegacy: false}

	for _, level := range []string{"", "off"} {
		t.Run("level="+level, func(t *testing.T) {
			cfg := BuildAnthropicThinkingFromMeta(meta, level, 4096)
			if got, _ := cfg.Thinking["type"].(string); got != "adaptive" {
				t.Fatalf("Thinking[\"type\"] = %q, want \"adaptive\" (adaptive-only model must never send \"disabled\")", got)
			}
			if cfg.OutputConfig == nil {
				t.Fatal("OutputConfig is nil, want {\"effort\":\"low\"} — an adaptive request with no effort set is a DIFFERENT invalid shape")
			}
			if got, _ := cfg.OutputConfig["effort"].(string); got != "low" {
				t.Errorf("OutputConfig[\"effort\"] = %q, want \"low\" (lowest effort, closest equivalent to \"off\")", got)
			}
			if cfg.MaxTokens < 16000 {
				t.Errorf("MaxTokens = %d, want >= 16000 (adaptive's own minimum)", cfg.MaxTokens)
			}
		})
	}
}

// TestBuildAnthropicThinkingFromMeta_LegacyCapableModelStillGetsDisabled
// confirms the fix is additive — a model that DOES still support the
// legacy shape (ThinkingLegacy=true, whether or not it also supports
// adaptive — confirmed live: claude-opus-4-6/claude-sonnet-4-6 report
// adaptive=true legacy=true; claude-sonnet-4-5-20250929/claude-haiku-4-5
// report adaptive=false legacy=true) must keep sending
// {"type":"disabled"} for level=="off"/"" exactly like before this fix —
// no behavior change for any model that still accepts it.
func TestBuildAnthropicThinkingFromMeta_LegacyCapableModelStillGetsDisabled(t *testing.T) {
	cases := []struct {
		name     string
		adaptive bool
	}{
		{"adaptive+legacy (opus-4-6 shape)", true},
		{"legacy-only, no adaptive (sonnet-4-5 shape)", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			meta := &types.ModelMeta{ThinkingAdaptive: c.adaptive, ThinkingLegacy: true}
			for _, level := range []string{"", "off"} {
				cfg := BuildAnthropicThinkingFromMeta(meta, level, 4096)
				if got, _ := cfg.Thinking["type"].(string); got != "disabled" {
					t.Errorf("level=%q: Thinking[\"type\"] = %q, want \"disabled\" (legacy-capable model must be unaffected by this fix)", level, got)
				}
				if cfg.OutputConfig != nil {
					t.Errorf("level=%q: OutputConfig = %+v, want nil (disabled shape never carries output_config)", level, cfg.OutputConfig)
				}
			}
		})
	}
}

// TestBuildAnthropicThinkingFull_NameHeuristicAdaptiveOnlyModelDegrades
// confirms the no-ModelMeta fallback path (BuildAnthropicThinkingFull,
// name-heuristic based via isAdaptiveOnly) gets the identical fix — it's a
// separate code path (used when a *types.ModelMeta genuinely isn't
// available, e.g. BuildAnthropicThinkingFromMeta(nil, ...)) that would
// otherwise still 400 the exact same way for an adaptive-only model
// matched by name (e.g. "claude-opus-5-5" per isAdaptiveOnly's own
// "opus-5" pattern).
func TestBuildAnthropicThinkingFull_NameHeuristicAdaptiveOnlyModelDegrades(t *testing.T) {
	cfg, err := BuildAnthropicThinkingFull("claude-opus-5-5", "off", 4096)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := cfg.Thinking["type"].(string); got != "adaptive" {
		t.Fatalf("model=claude-opus-5-5 (name heuristic): Thinking[\"type\"] = %q, want \"adaptive\"", got)
	}
	if cfg.OutputConfig == nil || cfg.OutputConfig["effort"] != "low" {
		t.Errorf("model=claude-opus-5-5 (name heuristic): OutputConfig = %+v, want {\"effort\":\"low\"}", cfg.OutputConfig)
	}
}

// TestBuildAnthropicThinkingFull_NonAdaptiveModelUnaffected confirms a
// plain non-adaptive model name (no ModelMeta, name-heuristic path) still
// gets the original "disabled" shape for level=="off"/"" — the fix must
// not regress the common case.
func TestBuildAnthropicThinkingFull_NonAdaptiveModelUnaffected(t *testing.T) {
	cfg, err := BuildAnthropicThinkingFull("claude-3-5-sonnet-20241022", "off", 4096)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := cfg.Thinking["type"].(string); got != "disabled" {
		t.Errorf("Thinking[\"type\"] = %q, want \"disabled\"", got)
	}
}

// ── resolveAnthropicEffort / EffortLevels-driven xhigh/max handling ───────
//
// Regression coverage for a real, confirmed-live bug: harness used to
// unconditionally translate a requested "xhigh" into Anthropic's own
// "max" value, for every adaptive model — which both silently escalated
// cost/latency beyond what was asked (xhigh != max) AND made Anthropic's
// genuine "max" level completely unreachable from harness. Confirmed live
// via a real GET /v1/models call (claude-oauth credentials) that Anthropic
// reports per-level support via capabilities.effort.<level>.supported —
// e.g. opus-4-6/sonnet-4-6 support "max" but NOT "xhigh" (xhigh was only
// added with Opus 4.7), while opus-4-7/opus-5.5/etc. support both.

func TestResolveAnthropicEffort_UnknownLevelsPassesThrough(t *testing.T) {
	// nil EffortLevels (provider reported nothing — e.g. a model this
	// account hasn't been served capabilities for) must not invent a
	// restriction that doesn't exist; pass the requested level through
	// unchanged, same as harness's behavior before EffortLevels existed.
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if got := resolveAnthropicEffort(level, nil); got != level {
			t.Errorf("resolveAnthropicEffort(%q, nil) = %q, want %q unchanged", level, got, level)
		}
	}
}

func TestResolveAnthropicEffort_FullSupportPassesThroughUnchanged(t *testing.T) {
	// opus-4-7/opus-5.5 shape: every level supported — xhigh must be sent
	// as xhigh, not silently rewritten to max.
	levels := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if got := resolveAnthropicEffort(level, levels); got != level {
			t.Errorf("resolveAnthropicEffort(%q, fully-supported) = %q, want %q (no downgrade needed)", level, got, level)
		}
	}
}

func TestResolveAnthropicEffort_XHighFallsBackToHighWhenUnsupported(t *testing.T) {
	// opus-4-6/sonnet-4-6 shape, confirmed live: xhigh:false, max:true.
	// Requesting xhigh must silently fall back to the highest level AT OR
	// BELOW it that IS supported — "high" here, never silently jumping
	// UP to "max" (that would be a bigger request than was ever asked for).
	levels := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": false, "max": true}
	if got := resolveAnthropicEffort("xhigh", levels); got != "high" {
		t.Errorf("resolveAnthropicEffort(xhigh, opus-4-6-shape) = %q, want %q", got, "high")
	}
	// max IS supported here and must still be reachable directly.
	if got := resolveAnthropicEffort("max", levels); got != "max" {
		t.Errorf("resolveAnthropicEffort(max, opus-4-6-shape) = %q, want %q", got, "max")
	}
}

func TestResolveAnthropicEffort_MaxFallsBackToHighWhenUnsupported(t *testing.T) {
	// opus-4-5 shape, confirmed live: xhigh:false, max:false (only
	// low/medium/high). Requesting "max" must degrade all the way to the
	// highest real support — "high".
	levels := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": false, "max": false}
	if got := resolveAnthropicEffort("max", levels); got != "high" {
		t.Errorf("resolveAnthropicEffort(max, opus-4-5-shape) = %q, want %q", got, "high")
	}
}

func TestResolveAnthropicEffort_UnknownNameOutsideOurLevelsPassesThrough(t *testing.T) {
	// Not one of harness's 5 known effort levels (shouldn't happen via
	// normal use, but keeps this function total/defensive) — passed
	// through as-is rather than mis-resolved against the ordered list.
	levels := map[string]bool{"low": true}
	if got := resolveAnthropicEffort("ultra", levels); got != "ultra" {
		t.Errorf("resolveAnthropicEffort(ultra, ...) = %q, want %q unchanged", got, "ultra")
	}
}

// TestBuildAnthropicThinkingFromMeta_EffortLevelsDriveRealFallback is the
// end-to-end regression test through the actual production entry point
// every adaptive request uses.
func TestBuildAnthropicThinkingFromMeta_EffortLevelsDriveRealFallback(t *testing.T) {
	meta := &types.ModelMeta{
		ThinkingAdaptive: true,
		ThinkingLegacy:   false,
		EffortLevels:     map[string]bool{"low": true, "medium": true, "high": true, "xhigh": false, "max": true},
	}
	cfg := BuildAnthropicThinkingFromMeta(meta, "xhigh", 4096)
	if got, _ := cfg.OutputConfig["effort"].(string); got != "high" {
		t.Errorf("effort = %q, want %q (xhigh unsupported on this model, falls back to high)", got, "high")
	}

	cfg = BuildAnthropicThinkingFromMeta(meta, "max", 4096)
	if got, _ := cfg.OutputConfig["effort"].(string); got != "max" {
		t.Errorf("effort = %q, want %q (max IS supported, sent directly)", got, "max")
	}
}
