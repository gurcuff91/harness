package llm

import "testing"

// TestTranslateThinkingLevel_DeepSeek locks in DeepSeek's own documented
// collapse rules (confirmed against official DeepSeek docs): only 3 real
// effort values exist server-side (low, high, max) and everything else
// collapses. "max" is one of harness's universal levels now and must be
// passed through unchanged — previously harness forced "xhigh" to "max"
// here itself, which is no longer needed now that "max" is a real,
// independently-selectable harness level. "low"/"medium" intentionally
// still fall through to "high" here — a pre-existing, separate quirk left
// untouched by this change (not in scope).
func TestTranslateThinkingLevel_DeepSeek(t *testing.T) {
	cases := []struct {
		level string
		want  string
	}{
		{"high", "high"},
		{"xhigh", "high"},
		{"max", "max"},
	}
	for _, c := range cases {
		if got := translateThinkingLevel("deepseek-v4-flash", c.level, false); got != c.want {
			t.Errorf("translateThinkingLevel(deepseek, %q) = %q, want %q", c.level, got, c.want)
		}
	}
}

// TestTranslateThinkingLevel_OSeries confirms o1/o3/o4 are unaffected by
// the xhigh/max addition — they have no concept of either and both still
// clamp to the pre-existing "high" default.
func TestTranslateThinkingLevel_OSeries(t *testing.T) {
	cases := []struct {
		level string
		want  string
	}{
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "high"},
		{"max", "high"},
	}
	for _, model := range []string{"o1", "o3", "o4-mini"} {
		for _, c := range cases {
			if got := translateThinkingLevel(model, c.level, false); got != c.want {
				t.Errorf("translateThinkingLevel(%s, %q) = %q, want %q", model, c.level, got, c.want)
			}
		}
	}
}

// TestTranslateThinkingLevel_OllamaOptedIn confirms Ollama's real,
// per-model "low"/"medium"/"high"/"max" reasoning_effort values (confirmed
// against Ollama's official /v1/chat/completions docs) are sent as-is when
// OllamaReasoningEffort is set, with xhigh clamping to Ollama's own ceiling
// ("max") rather than being dropped entirely.
func TestTranslateThinkingLevel_OllamaOptedIn(t *testing.T) {
	cases := []struct {
		level string
		want  string
	}{
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "max"},
		{"max", "max"},
	}
	for _, c := range cases {
		if got := translateThinkingLevel("llama3.3", c.level, true); got != c.want {
			t.Errorf("translateThinkingLevel(ollama-opted-in, %q) = %q, want %q", c.level, got, c.want)
		}
	}
}

// TestTranslateThinkingLevel_GenericProviderWithoutOllamaOptInIsUnaffected
// confirms a plain OpenAI-compatible provider (MiniMax, CustomOpenAI,
// OpenCode Go — none of which set OllamaReasoningEffort) gets NO
// reasoning_effort value at all, exactly as before this change — the
// opt-in flag is what prevents model-ID guessing from misfiring on these.
func TestTranslateThinkingLevel_GenericProviderWithoutOllamaOptInIsUnaffected(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if got := translateThinkingLevel("some-generic-model", level, false); got != "" {
			t.Errorf("translateThinkingLevel(generic, %q, ollamaOptIn=false) = %q, want \"\" (unaffected)", level, got)
		}
	}
}
