package llm

import (
	"crypto/sha256"
	"strings"
)

// ToolIDFor converts a provider-native tool_use ID into a harness-canonical
// "toolu_<24chars>" form. The conversion is deterministic: the same input
// always produces the same output. This means:
//
//   - Anthropic IDs ("toolu_01HXYZ...") round-trip identically — they already
//     use the "toolu_" prefix and a 24-char base62 body, which isCanonicalID
//     recognizes.
//   - OpenAI IDs ("call_xxx") become a stable "toolu_HASH" derived from
//     "call_xxx" — re-derived on resume, no mapping table needed.
//   - Gemini IDs ("functions.Bash:1") become a stable "toolu_HASH"
//     derived from "functions.Bash:1".
//   - Arbitrary strings (manual edits, future providers) work too.
//
// All providers accept "toolu_<alphanumeric>" because the character set
// is [a-zA-Z0-9_-], which is the union of every provider's ID constraints.
// Anthropic's regex ^[a-zA-Z0-9_-]+$ is the strictest, and toolu_<base62>
// satisfies it (the underscore is allowed).
//
// The harness store persists tool_use and tool_result IDs in this canonical
// form, so a session resumed against any provider (including Anthropic)
// never trips a 400 on an ID that another provider's native format
// produced. Existing on-disk sessions written before this normalization
// must be migrated by an external script; the code assumes they are already
// canonical.
func ToolIDFor(seed string) string {
	if isCanonicalID(seed) {
		return seed // already toolu_<canonical>, don't re-hash
	}
	h := sha256.Sum256([]byte(seed))
	return "toolu_" + base62Encode(h[:18]) // 24 base62 chars = ~142 bits
}

// isCanonicalID reports whether s is already a harness-canonical tool ID:
// "toolu_" followed by 24 base62 characters. This matches Anthropic's
// native format (toolu_ + 24 base62 chars), so Anthropic-generated IDs
// pass through unchanged; harness-generated IDs (from ToolIDFor) use the
// same shape.
func isCanonicalID(s string) bool {
	const prefix = "toolu_"
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	body := s[len(prefix):]
	if len(body) != 24 {
		return false
	}
	for i := 0; i < len(body); i++ {
		if !isBase62Char(body[i]) {
			return false
		}
	}
	return true
}

// isBase62Char reports whether b is a character in the base62 alphabet
// [0-9A-Za-z]. The underscore is intentionally excluded: toolu_<body>
// keeps the body strictly base62 so Anthropic's ^[a-zA-Z0-9_-]+$ regex
// (which allows the underscore only as a separator inside the ID, not
// as a body char) is satisfied — though in practice the regex accepts
// underscores anywhere, keeping the body base62 is cleaner.
func isBase62Char(b byte) bool {
	switch {
	case b >= '0' && b <= '9':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= 'a' && b <= 'z':
		return true
	}
	return false
}

// base62Encode encodes 18 bytes (144 bits) into 24 base62 characters.
// 18 bytes → 144 bits → log2(62) ≈ 5.95 bits/char → 24 chars. The
// encoding is big-endian, left-padded so the output length is stable
// (always 24 chars for 18-byte inputs), which keeps the canonical ID
// format predictable and matches Anthropic's native toolu_ length.
func base62Encode(b []byte) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	if len(b) != 18 {
		// Defensive: callers pass sha256[:18]. Fall back to whatever we got.
		b = b[:18]
	}
	// Treat the 18 bytes as a big-endian integer, then convert to base62.
	var val [18]byte
	copy(val[:], b)
	var out []byte
	// Repeatedly divide the big-endian number by 62, collecting remainders.
	for i := 0; i < 24; i++ {
		// Divide val by 62 in place.
		var rem uint
		for j := 0; j < 18; j++ {
			acc := uint(val[j]) + rem*256
			val[j] = byte(acc / 62)
			rem = acc % 62
		}
		out = append(out, alphabet[rem])
	}
	// Reverse so the most-significant digit is first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
