package providers

import (
	"encoding/json"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// capsFromJSON builds a capabilities map[string]any from a raw JSON
// literal — mirrors exactly how encoding/json decodes GET /v1/models'
// response into the map[string]any field fetchAnthropicModels declares.
func capsFromJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var caps map[string]any
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		t.Fatalf("invalid test JSON: %v", err)
	}
	return caps
}

// TestApplyAnthropicCapabilities_Opus47Shape reproduces the EXACT
// capabilities.effort shape captured live from a real GET /v1/models call
// (claude-oauth credentials) against claude-opus-4-7 — every level
// supported, including xhigh (added with Opus 4.7).
func TestApplyAnthropicCapabilities_Opus47Shape(t *testing.T) {
	caps := capsFromJSON(t, `{
		"image_input": {"supported": true},
		"thinking": {
			"supported": true,
			"types": {
				"adaptive": {"supported": true},
				"enabled": {"supported": false}
			}
		},
		"effort": {
			"supported": true,
			"low": {"supported": true},
			"medium": {"supported": true},
			"high": {"supported": true},
			"xhigh": {"supported": true},
			"max": {"supported": true}
		}
	}`)
	var meta types.ModelMeta
	applyAnthropicCapabilities(&meta, caps)

	if !meta.Vision {
		t.Error("Vision = false, want true")
	}
	if !meta.ThinkingAdaptive || meta.ThinkingLegacy {
		t.Errorf("ThinkingAdaptive=%v ThinkingLegacy=%v, want true/false (opus-4-7 has dropped legacy)", meta.ThinkingAdaptive, meta.ThinkingLegacy)
	}
	want := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	for lvl, v := range want {
		if meta.EffortLevels[lvl] != v {
			t.Errorf("EffortLevels[%q] = %v, want %v", lvl, meta.EffortLevels[lvl], v)
		}
	}
}

// TestApplyAnthropicCapabilities_Opus46Shape reproduces the live-captured
// shape for claude-opus-4-6/claude-sonnet-4-6: max IS supported but xhigh
// is NOT (xhigh didn't exist yet on this model generation) — the key
// asymmetric case that makes resolveAnthropicEffort's fallback necessary.
func TestApplyAnthropicCapabilities_Opus46Shape(t *testing.T) {
	caps := capsFromJSON(t, `{
		"effort": {
			"supported": true,
			"low": {"supported": true},
			"medium": {"supported": true},
			"high": {"supported": true},
			"xhigh": {"supported": false},
			"max": {"supported": true}
		}
	}`)
	var meta types.ModelMeta
	applyAnthropicCapabilities(&meta, caps)

	if meta.EffortLevels["xhigh"] {
		t.Error("EffortLevels[xhigh] = true, want false (opus-4-6 predates xhigh)")
	}
	if !meta.EffortLevels["max"] {
		t.Error("EffortLevels[max] = false, want true")
	}
}

// TestApplyAnthropicCapabilities_LegacyOnlyModelShape reproduces the
// live-captured shape for claude-haiku-4-5/claude-sonnet-4-5 — pure
// legacy (budget_tokens) models where the ENTIRE effort object reports
// unsupported, including the top-level "supported" flag.
func TestApplyAnthropicCapabilities_LegacyOnlyModelShape(t *testing.T) {
	caps := capsFromJSON(t, `{
		"effort": {
			"supported": false,
			"low": {"supported": false},
			"medium": {"supported": false},
			"high": {"supported": false},
			"xhigh": {"supported": false},
			"max": {"supported": false}
		}
	}`)
	var meta types.ModelMeta
	applyAnthropicCapabilities(&meta, caps)

	for _, lvl := range []string{"low", "medium", "high", "xhigh", "max"} {
		if meta.EffortLevels[lvl] {
			t.Errorf("EffortLevels[%q] = true, want false (legacy-only model)", lvl)
		}
	}
}

// TestApplyAnthropicCapabilities_NilAndMissingEffortAreSafe confirms a nil
// capabilities map (model had none at all) and a capabilities map that
// simply lacks an "effort" key entirely both leave EffortLevels nil
// (unknown, per its own doc comment contract) rather than panicking or
// synthesizing a misleading empty-but-present map.
func TestApplyAnthropicCapabilities_NilAndMissingEffortAreSafe(t *testing.T) {
	var meta types.ModelMeta
	applyAnthropicCapabilities(&meta, nil)
	if meta.EffortLevels != nil {
		t.Errorf("EffortLevels = %+v, want nil for a nil capabilities map", meta.EffortLevels)
	}

	var meta2 types.ModelMeta
	applyAnthropicCapabilities(&meta2, capsFromJSON(t, `{"image_input": {"supported": true}}`))
	if meta2.EffortLevels != nil {
		t.Errorf("EffortLevels = %+v, want nil when capabilities has no \"effort\" key", meta2.EffortLevels)
	}
}
