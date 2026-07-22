package llm

import "encoding/json"

// jsonMarshal is a tiny test helper — keeps the SSE-shaped test data terse.
func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
