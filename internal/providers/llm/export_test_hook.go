package llm

import (
	"encoding/json"

	"github.com/gurcuff91/harness/types"
)

// TranslateMessageToOpenAIForTest exposes the OpenAI translation to other
// packages' tests (the providers' "no timestamp on the wire" guard).
func TranslateMessageToOpenAIForTest(msg types.Message) []json.RawMessage {
	return translateMessageToOpenAI(msg)
}
