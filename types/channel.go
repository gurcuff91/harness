package types

// InputChannel asks a session to listen to one external conversation while it
// is open: Type names the implementation registered on the agent ("slack"),
// Ref the conversation in that implementation's own terms (for Slack: a
// channel id, a DM id, a user id, or a channel name with or without "#").
// It is resolved to a stable id when the session opens; nothing is persisted.
type InputChannel struct {
	Type string `json:"type"`
	Ref  string `json:"ref"`
}
