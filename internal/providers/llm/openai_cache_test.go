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

// ── cache WRITES (created_cache_tokens) ─────────────────────────────────────────────────────────────

// Some OpenAI-dialect providers report the tokens a request WROTE to the prompt cache beside the ones it
// read: Synthetic does it for DeepSeek and Kimi as prompt_tokens_details.created_cache_tokens (measured: the
// same 3645-token prompt, first call created_cache_tokens=3584 and cached_tokens=0, second call
// cached_tokens=3584 and created_cache_tokens=0). Like cached_tokens it is a SUBSET of prompt_tokens (the
// prompt_tokens of both calls is identical), so the session, which adds fresh + read + write, needs
// fresh = prompt - cached - created or the written part is counted twice.
func TestACacheWriteIsReportedAndTakenOutOfTheFreshCount(t *testing.T) {
	resp, events := feedParseStream(t, usageStream(
		`{"prompt_tokens":3645,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":0,"created_cache_tokens":3584},"completion_tokens_details":{"reasoning_tokens":9}}`))
	if resp.Usage.InputTokens != 61 || resp.Usage.CacheRead != 0 || resp.Usage.CacheWrite != 3584 || resp.Usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v, want fresh 61, read 0, write 3584, out 12", resp.Usage)
	}
	if sum := resp.Usage.InputTokens + resp.Usage.CacheRead + resp.Usage.CacheWrite; sum != 3645 {
		t.Errorf("fresh + read + write = %d, want the whole prompt (3645): the context fill would be wrong", sum)
	}
	// the event the session reads carries the write too (that is what the footer's W shows)
	got := false
	for _, e := range events {
		if e.CacheWrite != 0 {
			got = true
			if e.InputTokens != 61 || e.CacheRead != 0 || e.CacheWrite != 3584 || e.OutputTokens != 12 {
				t.Errorf("usage event = in %d read %d write %d out %d", e.InputTokens, e.CacheRead, e.CacheWrite, e.OutputTokens)
			}
		}
	}
	if !got {
		t.Error("no usage event carried the cache write")
	}
}

// The second call of that pair: all read, nothing written.
func TestACacheHitHasNoWrite(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":3645,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":3584,"created_cache_tokens":0}}`))
	if resp.Usage.InputTokens != 61 || resp.Usage.CacheRead != 3584 || resp.Usage.CacheWrite != 0 {
		t.Errorf("usage = %+v, want fresh 61, read 3584, write 0", resp.Usage)
	}
}

// A request can read part of the prompt from the cache and write another part: all three are disjoint
// slices of the same prompt.
func TestAPromptThatReadsAndWritesSplitsIntoThreeParts(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":10000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":6000,"created_cache_tokens":3000}}`))
	if resp.Usage.InputTokens != 1000 || resp.Usage.CacheRead != 6000 || resp.Usage.CacheWrite != 3000 {
		t.Errorf("usage = %+v, want fresh 1000, read 6000, write 3000", resp.Usage)
	}
}

// Only the LAST cumulative usage object counts, split whole: a write reported on an earlier chunk must not
// survive into a later one that does not carry it.
func TestACacheWriteFromAnEarlierChunkDoesNotSurvive(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":500,"completion_tokens":1,"prompt_tokens_details":{"created_cache_tokens":400}}`,
		`{"prompt_tokens":500,"completion_tokens":7}`))
	if resp.Usage.InputTokens != 500 || resp.Usage.CacheWrite != 0 {
		t.Errorf("usage = %+v, want fresh 500, write 0: a stale write from an earlier chunk was kept", resp.Usage)
	}
}

// Nothing changes for a provider that reports no write: details absent, null, of another type, or
// created_cache_tokens absent, null, zero or of another type.
func TestNoCacheWriteDetailsMeansNothingChanges(t *testing.T) {
	for name, usage := range map[string]string{
		"no details":                 `{"prompt_tokens":120,"completion_tokens":30}`,
		"details without created":    `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":0}}`,
		"created is null":            `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"created_cache_tokens":null}}`,
		"created is zero":            `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"created_cache_tokens":0}}`,
		"created has the wrong type": `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"created_cache_tokens":"many"}}`,
		"details is null":            `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":null}`,
		"details has the wrong type": `{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":"none"}`,
	} {
		resp, _ := feedParseStream(t, usageStream(usage))
		if resp.Usage.InputTokens != 120 || resp.Usage.CacheWrite != 0 || resp.Usage.CacheRead != 0 {
			t.Errorf("%s: usage = %+v, want fresh 120, no cache", name, resp.Usage)
		}
	}
}

// An inconsistent report must never make a part negative or count more than the prompt: a negative write is
// ignored, a write larger than the prompt is clamped, and read + write together larger than the prompt
// are clamped in order (the read first, then the write gets what is left).
func TestInconsistentCacheNumbersAreClamped(t *testing.T) {
	for name, c := range map[string]struct {
		usage                string
		fresh, read, written int
	}{
		"negative write":               {`{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"created_cache_tokens":-40}}`, 100, 0, 0},
		"write larger than the prompt": {`{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"created_cache_tokens":250}}`, 0, 0, 100},
		"read plus write larger":       {`{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":70,"created_cache_tokens":70}}`, 0, 70, 30},
		"read alone already the whole": {`{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":100,"created_cache_tokens":50}}`, 0, 100, 0},
		"both negative":                {`{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":-5,"created_cache_tokens":-5}}`, 100, 0, 0},
		"a zero prompt with a write":   {`{"prompt_tokens":0,"completion_tokens":1,"prompt_tokens_details":{"created_cache_tokens":9}}`, 0, 0, 0},
	} {
		resp, _ := feedParseStream(t, usageStream(c.usage))
		u := resp.Usage
		if u.InputTokens < 0 || u.CacheRead < 0 || u.CacheWrite < 0 {
			t.Errorf("%s: a part is negative: %+v", name, u)
		}
		if u.InputTokens != c.fresh || u.CacheRead != c.read || u.CacheWrite != c.written {
			t.Errorf("%s: usage = %+v, want fresh %d read %d write %d", name, u, c.fresh, c.read, c.written)
		}
		if sum := u.InputTokens + u.CacheRead + u.CacheWrite; sum > 100 && c.fresh+c.read+c.written <= 100 {
			t.Errorf("%s: the three parts add up to %d, more than the prompt", name, sum)
		}
	}
}

// OpenAI's own detail objects carry other numbers beside the cache (audio, image, reasoning): they are not
// the cache and must not be mistaken for it.
func TestOtherDetailFieldsAreNotTheCache(t *testing.T) {
	resp, _ := feedParseStream(t, usageStream(
		`{"prompt_tokens":300,"completion_tokens":4,"prompt_tokens_details":{"audio_tokens":10,"image_tokens":16,"text_tokens":274},"completion_tokens_details":{"reasoning_tokens":2,"created_cache_tokens":99}}`))
	if resp.Usage.InputTokens != 300 || resp.Usage.CacheRead != 0 || resp.Usage.CacheWrite != 0 {
		t.Errorf("usage = %+v, want fresh 300 and no cache (created_cache_tokens under completion_tokens_details is not a prompt write)", resp.Usage)
	}
}

// A broken provider can send a NEGATIVE prompt_tokens. No part may ever be negative whatever arrives: a
// negative number would subtract from the session's running totals and from its cost. With nothing to
// split, every part is zero (prompt_tokens itself is what the provider said, and is reported as it came
// only when it is not negative).
func TestANegativePromptNeverProducesANegativePart(t *testing.T) {
	for name, usage := range map[string]string{
		"negative prompt, no cache":      `{"prompt_tokens":-50,"completion_tokens":3}`,
		"negative prompt with a read":    `{"prompt_tokens":-50,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":20}}`,
		"negative prompt with a write":   `{"prompt_tokens":-50,"completion_tokens":3,"prompt_tokens_details":{"created_cache_tokens":20}}`,
		"negative prompt with both":      `{"prompt_tokens":-50,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":20,"created_cache_tokens":20}}`,
		"negative prompt, negative both": `{"prompt_tokens":-50,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":-20,"created_cache_tokens":-20}}`,
	} {
		resp, _ := feedParseStream(t, usageStream(usage))
		u := resp.Usage
		if u.CacheRead < 0 || u.CacheWrite < 0 {
			t.Errorf("%s: a cache part is negative: %+v", name, u)
		}
		if u.CacheRead != 0 || u.CacheWrite != 0 {
			t.Errorf("%s: usage = %+v, want no cache parts when there is no prompt to hold them", name, u)
		}
	}
}

// clampInt is the one place the bounds are enforced, and it is exact: inside is untouched, outside is cut,
// and when the bounds cross (hi below lo) the LOWER bound wins, never a number below it.
func TestClampIntBoundsAreExact(t *testing.T) {
	for _, c := range []struct{ v, lo, hi, want int }{
		{5, 0, 10, 5}, {0, 0, 10, 0}, {10, 0, 10, 10}, {-1, 0, 10, 0}, {11, 0, 10, 10},
		{5, 0, 0, 0}, {5, 0, -3, 0}, {-9, 0, -3, 0}, {-3, 0, -3, 0}, {7, 2, 1, 2},
	} {
		if got := clampInt(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("clampInt(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}
