package llm

// TokenizerFamily identifies the tokenizer family a provider uses.
// It drives the chars-per-token divisor for estimating S (system prompt) and
// T (tools) in ContextBreakdown. C (conversation) is derived from the actual
// provider-reported token count, so no message estimation is needed.
//
// Two families cover all current providers:
//   - Anthropic (Claude): SentencePiece, ~6 chars/token for English/code.
//   - OpenAI-compatible:  cl100k_base BPE, ~4 chars/token for English/code.
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

// FamilyForProvider maps a provider Name() to its tokenizer family.
// Unknown providers fall back to OpenAI.
func FamilyForProvider(providerName string) TokenizerFamily {
	switch providerName {
	case "anthropic", "claude-oauth":
		return FamilyAnthropic
	default:
		return FamilyOpenAI
	}
}
