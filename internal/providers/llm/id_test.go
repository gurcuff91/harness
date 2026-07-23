package llm

import (
	"regexp"
	"strings"
	"testing"
)

// anthropicIDPattern is the regex Anthropic enforces on tool_use.id values.
// Every canonical ID we produce must satisfy it so a session persisted by
// any provider can be resumed against Anthropic without a 400.
var anthropicIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func TestToolIDForAnthropicNative(t *testing.T) {
	// Anthropic-native IDs already use the "toolu_" prefix and a 24-char
	// base62 body. They must round-trip unchanged so we don't break
	// correlation when resuming an Anthropic session against Anthropic.
	in := "toolu_01MbfnG6LeC2NWRkCFxKVHUZ"
	out := ToolIDFor(in)
	if out != in {
		t.Errorf("Anthropic-native ID changed: got %q, want %q", out, in)
	}
}

func TestToolIDForOpenAIDeterministic(t *testing.T) {
	// OpenAI "call_xxx" IDs must always map to the same canonical ID.
	a := ToolIDFor("call_abc123")
	b := ToolIDFor("call_abc123")
	if a != b {
		t.Errorf("OpenAI ID not deterministic: %q vs %q", a, b)
	}
	if a == "call_abc123" {
		t.Errorf("OpenAI ID was not canonicalized")
	}
}

func TestToolIDForGeminiDeterministic(t *testing.T) {
	// Gemini "functions.X:N" IDs (with dots and colons) must always map
	// to the same canonical ID and be valid for Anthropic.
	a := ToolIDFor("functions.Bash:1")
	b := ToolIDFor("functions.Bash:1")
	if a != b {
		t.Errorf("Gemini ID not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "toolu_") {
		t.Errorf("Gemini ID not canonicalized: %q", a)
	}
	if !anthropicIDPattern.MatchString(a) {
		t.Errorf("Gemini-derived ID fails Anthropic regex: %q", a)
	}
}

func TestToolIDForSatisfiesAnthropicRegex(t *testing.T) {
	// Every canonical ID we produce — regardless of seed — must satisfy
	// Anthropic's tool_use.id regex.
	seeds := []string{
		"call_abc123",
		"functions.Bash:1",
		"functions.mcp__kaiban__get_card:0",
		"req_011CdKUi1tcawZqvcMGhku9B",
		"random-id-with-dots.and:colons",
		"",
		"unicode-émoji-🚀",
	}
	for _, s := range seeds {
		out := ToolIDFor(s)
		if !strings.HasPrefix(out, "toolu_") {
			t.Errorf("seed %q: missing toolu_ prefix: %q", s, out)
			continue
		}
		if !anthropicIDPattern.MatchString(out) {
			t.Errorf("seed %q: ID %q fails Anthropic regex", s, out)
		}
	}
}

func TestToolIDForDistinctSeeds(t *testing.T) {
	// Different seeds must produce different canonical IDs (collision
	// resistance). We use a handful of clearly distinct seeds.
	seeds := []string{
		"call_a",
		"call_b",
		"functions.Bash:1",
		"functions.Bash:2",
	}
	seen := map[string]bool{}
	for _, s := range seeds {
		out := ToolIDFor(s)
		if seen[out] {
			t.Errorf("collision: seed %q produced already-seen ID %q", s, out)
		}
		seen[out] = true
	}
}

func TestToolIDForCanonicalLength(t *testing.T) {
	// Canonical IDs are "toolu_" (6) + 24 base62 chars = 30 total, matching
	// Anthropic's native format.
	out := ToolIDFor("call_abc123")
	if len(out) != 30 {
		t.Errorf("canonical ID length: got %d, want 30 (got %q)", len(out), out)
	}
}

func TestToolIDForEmptySeed(t *testing.T) {
	// Even an empty seed must produce a valid canonical ID (no panic).
	out := ToolIDFor("")
	if !strings.HasPrefix(out, "toolu_") {
		t.Fatalf("empty seed: missing toolu_ prefix: %q", out)
	}
	if !anthropicIDPattern.MatchString(out) {
		t.Errorf("empty seed: ID %q fails Anthropic regex", out)
	}
}

func TestIsCanonicalID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"toolu_01MbfnG6LeC2NWRkCFxKVHUZ", true},   // valid: prefix + 24 base62
		{"toolu_01MbfnG6LeC2NWRkCFxKVHU", false},   // too short (23)
		{"toolu_01MbfnG6LeC2NWRkCFxKVHUZZ", false}, // too long (25)
		{"toolu_01MbfnG6LeC2NWRkCFxKVHU!", false},  // non-base62 char
		{"call_abc123", false},                     // wrong prefix
		{"functions.Bash:1", false},                // wrong prefix
		{"", false},                                // empty
	}
	for _, c := range cases {
		if got := isCanonicalID(c.id); got != c.want {
			t.Errorf("isCanonicalID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
