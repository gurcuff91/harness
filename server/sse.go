package server

import (
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// formatEvent converts an agent event to a JSON SSE data line (the payload is
// types.WireEvent, shared with the other frontends). Returns nil for event
// types not exposed.
func formatEvent(e types.Event) []byte {
	payload, ok := types.WireEvent(e)
	if !ok {
		return nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return []byte(fmt.Sprintf("data: %s\n\n", string(b)))
}
