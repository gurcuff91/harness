package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// usageStream builds a minimal OpenAI-dialect stream whose events carry the given usage objects, one per
// event, then a final usage-only event (choices: []) and [DONE] — the shape Synthetic produces.
func usageStream(usages ...string) string {
	var b strings.Builder
	for _, u := range usages {
		b.WriteString(`data: {"choices":[{"index":0,"delta":{"content":"x"}}],"usage":` + u + "}\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// OpenAI-dialect usage is INCLUSIVE: prompt_tokens is the whole prompt and cached_tokens is a SUBSET of
// it (measured on Synthetic: prompt_tokens=7015, cached_tokens=6976). The harness's accounting is
// Anthropic's: Input is the FRESH tokens and CacheRead is added on top (cost = Input*inPrice +
// CacheRead*cachePrice; context = Input + CacheRead + CacheWrite). Passing both numbers through as they
// arrive would bill the cached part twice and inflate the context percentage, so the parser subtracts.
func TestACachedPromptIsReportedAsFreshPlusCacheRead(t *testing.T) {
	resp, events := feedParseStream(t, usageStream(
		`{"prompt_tokens":7015,"completion_tokens":2,"total_tokens":7017,"prompt_tokens_details":{"cached_tokens":6976}}`))
	if resp.Usage.InputTokens != 39 || resp.Usage.CacheRead != 6976 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want input=39 (fresh) cacheRead=6976 output=2", resp.Usage)
	}
	if resp.Usage.InputTokens+resp.Usage.CacheRead != 7015 {
		t.Errorf("fresh + cached = %d, want the whole prompt (7015): the context fill would be wrong", resp.Usage.InputTokens+resp.Usage.CacheRead)
	}
	_ = events
}

// The event the session reads (StreamUsage) carries the same split: that is what updateStats turns into
// cost and context percentage.
func TestTheUsageEventCarriesTheSplit(t *testing.T) {
	_, events := feedParseStream(t, usageStream(
		`{"prompt_tokens":1000,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":900}}`))
	found := false
	for _, e := range events {
		if e.InputTokens != 0 || e.CacheRead != 0 || e.OutputTokens != 0 {
			found = true
			if e.InputTokens != 100 || e.CacheRead != 900 || e.OutputTokens != 10 {
				t.Errorf("usage event = in %d, cacheRead %d, out %d; want 100, 900, 10", e.InputTokens, e.CacheRead, e.OutputTokens)
			}
		}
	}
	if !found {
		t.Fatal("no usage event was emitted")
	}
}

// Synthetic reports usage on EVERY chunk and it is CUMULATIVE, ending in a usage-only chunk with
// choices: []. Only the LAST one counts, and the split must be taken from that same last object — never a
// mix of one chunk's prompt_tokens and another's cached_tokens.
func TestOnlyTheLastCumulativeUsageCounts(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":500,"completion_tokens":0}`,
		`{"prompt_tokens":500,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":100}}`,
		`{"prompt_tokens":500,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":480}}`))
	if resp.Usage.InputTokens != 20 || resp.Usage.CacheRead != 480 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want input=20 cacheRead=480 output=7 (the last chunk's, whole)", resp.Usage)
	}
}

// A later usage object WITHOUT the details must not leave the earlier chunk's cached count behind:
// each usage object is the whole truth about the request.
func TestALaterUsageWithoutDetailsResetsTheCacheRead(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":500,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":400}}`,
		`{"prompt_tokens":500,"completion_tokens":7}`))
	if resp.Usage.InputTokens != 500 || resp.Usage.CacheRead != 0 {
		t.Errorf("usage = %+v, want input=500 cacheRead=0: a stale cached count from an earlier chunk was kept", resp.Usage)
	}
}

// Providers that do not report a cache (MiniMax, OpenCode Go, most custom ones) are untouched: no
// details, no change.
func TestNoCacheDetailsMeansNothingChanges(t *testing.T) {
	for name, usage := range map[string]string{
		"plain":                      `{"prompt_tokens":120,"completion_tokens":30}`,
		"details without cached":     `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"image_tokens":16}}`,
		"details is null":            `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":null}`,
		"cached is null":             `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":null}}`,
		"cached is zero":             `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":0}}`,
		"details has the wrong type": `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":"none"}`,
		"cached has the wrong type":  `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":"many"}}`,
	} {
		resp, _ := feedParseStream(t, usageStream(usage))
		if resp.Usage.InputTokens != 120 || resp.Usage.CacheRead != 0 || resp.Usage.OutputTokens != 30 {
			t.Errorf("%s: usage = %+v, want input=120 cacheRead=0 output=30", name, resp.Usage)
		}
	}
}

// An inconsistent report (cached larger than the whole prompt) must never produce a NEGATIVE fresh count:
// a negative number would subtract from the session's running total and from its cost.
func TestCachedLargerThanPromptNeverGoesNegative(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":250}}`))
	if resp.Usage.InputTokens < 0 {
		t.Fatalf("fresh input is negative (%d)", resp.Usage.InputTokens)
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.CacheRead != 100 {
		t.Errorf("usage = %+v, want input=0 cacheRead=100 (cached clamped to the whole prompt)", resp.Usage)
	}
	// and a negative cached_tokens is not a refund
	resp, _ = feedParseStream(t, usageStream(
		`{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":-30}}`))
	if resp.Usage.InputTokens != 100 || resp.Usage.CacheRead != 0 {
		t.Errorf("negative cached_tokens: usage = %+v, want input=100 cacheRead=0", resp.Usage)
	}
}

// Everything cached: fresh is zero, not 'unknown'.
func TestAFullyCachedPromptHasNoFreshTokens(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":2048,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2048}}`))
	if resp.Usage.InputTokens != 0 || resp.Usage.CacheRead != 2048 {
		t.Errorf("usage = %+v, want input=0 cacheRead=2048", resp.Usage)
	}
}

// ── VerbatimReasoningEffort (Synthetic) ─────────────────────────────────────────────────────────────

func effortBody(t *testing.T, model, level string, verbatim bool) map[string]any {
	t.Helper()
	b, err := buildOpenAIBody(&OpenAIRequest{
		Request:                 &types.Request{Model: model, ThinkingLevel: level, Messages: []types.Message{types.NewUserTextMessage("hi")}},
		VerbatimReasoningEffort: verbatim,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Opted in, the level goes out as reasoning_effort exactly ("off" as "none"), for EVERY model, DeepSeek
// included, with none of the fields the default path adds.
func TestVerbatimEffortSendsTheLevelAsItIs(t *testing.T) {
	for _, model := range []string{"hf:zai-org/GLM-4.7-Flash", "hf:deepseek-ai/DeepSeek-V4.1-Flash-1017", "o3-mini", "x"} {
		for level, want := range map[string]string{"off": "none", "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max"} {
			m := effortBody(t, model, level, true)
			if m["reasoning_effort"] != want {
				t.Errorf("%s/%s: reasoning_effort = %v, want %q", model, level, m["reasoning_effort"], want)
			}
			for _, k := range []string{"think", "thinking"} {
				if _, ok := m[k]; ok {
					t.Errorf("%s/%s: %q was added", model, level, k)
				}
			}
		}
	}
	// no level, nothing
	m := effortBody(t, "hf:zai-org/GLM-4.7-Flash", "", true)
	for _, k := range []string{"reasoning_effort", "think", "thinking"} {
		if _, ok := m[k]; ok {
			t.Errorf("%q sent with no level", k)
		}
	}
}

// Not opted in, NOTHING changed: the DeepSeek rule and the think flag are exactly what they were.
func TestWithoutTheOptInTheDefaultPathIsUnchanged(t *testing.T) {
	m := effortBody(t, "deepseek-x", "low", false)
	if m["reasoning_effort"] != "high" || m["think"] != true {
		t.Errorf("deepseek low: reasoning_effort=%v think=%v, want high / true", m["reasoning_effort"], m["think"])
	}
	if th, _ := m["thinking"].(map[string]any); th["type"] != "enabled" {
		t.Errorf("deepseek low: thinking = %v, want enabled", m["thinking"])
	}
	m = effortBody(t, "deepseek-x", "off", false)
	if m["think"] != false {
		t.Errorf("deepseek off: think = %v, want false", m["think"])
	}
	if th, _ := m["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Errorf("deepseek off: thinking = %v, want disabled", m["thinking"])
	}
	m = effortBody(t, "gpt-4o", "high", false)
	if m["think"] != true {
		t.Errorf("a generic model: think = %v, want true", m["think"])
	}
	if _, ok := m["reasoning_effort"]; ok {
		t.Errorf("a generic model gets reasoning_effort=%v, want none (the default path sends none)", m["reasoning_effort"])
	}
}
