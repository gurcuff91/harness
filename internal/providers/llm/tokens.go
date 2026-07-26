package llm

import (
	"encoding/json"
	"unicode"

	"github.com/gurcuff91/harness/types"
)

// TokenizerFamily identifies the tokenizer family a provider uses. It drives
// the chars-per-token divisor and which wire-format translator is used when
// estimating conversation token counts.
//
// Two families cover all current providers:
//   - Anthropic (Claude): SentencePiece, ~6 chars/token for English/code.
//   - OpenAI-compatible:  cl100k_base BPE, ~4 chars/token for English/code.
//
// Emojis and non-ASCII unicode are corrected independently of the family —
// they tokenize as more tokens than their byte count suggests.
type TokenizerFamily int

const (
	// FamilyAnthropic covers anthropic and claude-oauth providers.
	FamilyAnthropic TokenizerFamily = iota
	// FamilyOpenAI covers openai, opencode-go, minimax, ollama, ollama-cloud,
	// and any other OpenAI-compatible provider.
	FamilyOpenAI
)

// CharsPerToken is the base divisor for each family. Empirically derived:
// Anthropic SentencePiece produces ~6 chars/token on code+English; OpenAI
// cl100k_base produces ~4 chars/token.
func (f TokenizerFamily) CharsPerToken() int {
	if f == FamilyAnthropic {
		return 6
	}
	return 4
}

// unexported alias used inside this package
func (f TokenizerFamily) charsPerToken() int { return f.CharsPerToken() }

// FamilyForProvider maps a provider Name() to its tokenizer family.
// Unknown providers fall back to OpenAI (the safer under-estimator for usage
// warnings — better to show less free space than more).
func FamilyForProvider(providerName string) TokenizerFamily {
	switch providerName {
	case "anthropic", "claude-oauth":
		return FamilyAnthropic
	default:
		return FamilyOpenAI
	}
}

// EstimateTokens estimates the token count for a plain text string under the
// given tokenizer family. It counts Unicode rune clusters (not bytes) and adds
// a bonus for emoji and non-ASCII runes, which tokenize as more tokens than
// their visual weight suggests.
func EstimateTokens(text string, family TokenizerFamily) int {
	if text == "" {
		return 0
	}
	runes := []rune(text)
	// Base estimate: rune count / chars_per_token
	base := len(runes) / family.charsPerToken()
	// Unicode bonus: emoji and non-ASCII chars each add ~0.5 extra tokens on
	// average (they split into 2-3 sub-word tokens in both families).
	bonus := 0
	for _, r := range runes {
		if r > 127 && !unicode.IsLetter(r) {
			bonus++ // emoji, symbols, CJK radicals
		}
	}
	return base + bonus/2
}

// EstimateMessage estimates the token count for a types.Message using the
// provider's actual wire format — not our internal JSON representation. This
// gives a much more accurate estimate because we measure exactly the bytes the
// model receives, then divide by the correct chars-per-token for the family.
//
// Wire format selection:
//   - FamilyAnthropic → TranslateMessageToAnthropic (tool_use / tool_result shape)
//   - FamilyOpenAI    → TranslateMessageToOpenAI    (function-call shape)
func EstimateMessage(msg types.Message, family TokenizerFamily) int {
	var wires []json.RawMessage
	switch family {
	case FamilyAnthropic:
		wires = TranslateMessageToAnthropic(msg)
	default:
		wires = translateMessageToOpenAI(msg)
	}
	total := 0
	cpt := family.charsPerToken()
	for _, w := range wires {
		// Count runes, not bytes — multi-byte chars shouldn't be over-penalised.
		total += len([]rune(string(w))) / cpt
	}
	return total
}

// EstimateMessages estimates the total token count for a slice of messages.
func EstimateMessages(msgs []types.Message, family TokenizerFamily) int {
	total := 0
	for _, msg := range msgs {
		total += EstimateMessage(msg, family)
	}
	return total
}
