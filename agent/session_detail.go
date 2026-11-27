package agent

import "github.com/gurcuff91/harness/agent/store"

// SessionDetail is a session's persisted meta plus what lives only on the
// runtime Session (or the Agent as a fallback): the shape every frontend
// (HTTP, RPC) reports when a session is opened or read. Its JSON is the wire
// format of those responses.
type SessionDetail struct {
	store.SessionMeta
	// MaxIterations is the turn's iteration budget (the TUI footer's
	// "(turn/max_iterations)").
	MaxIterations int `json:"max_iterations"`
	// ModelFallback is present when the session was reopened on a different
	// model than it was saved with, because the saved model's provider was
	// unavailable (disconnected, credentials removed, model retired). `model`
	// is then the model it runs on now (and is saved with from now on); this
	// says what it was and why it changed. Omitted in the normal case.
	ModelFallback *ModelFallback `json:"model_fallback,omitempty"`
	// MissingProfiles are bound profiles that are not registered right now:
	// skipped (the session works without them), their names kept. Omitted
	// when none.
	MissingProfiles []string `json:"missing_profiles,omitempty"`
	// Channels are the external conversations the live session listens to.
	// Omitted when none (and for a persisted session: channels are never
	// stored).
	Channels []ChannelInfo `json:"channels,omitempty"`
	// Subagents are the names of the sub-agents the live session has running
	// right now, in launch order. Omitted when none (and for a persisted
	// session). It is the state a frontend that connects late starts from; the
	// subagent_start / subagent_stop events keep it up to date afterwards.
	Subagents []string `json:"subagents,omitempty"`
}

// Detail is the session's detail. Like Meta() it is lock-free: safe to call
// from anywhere, including while a turn runs.
func (s *Session) Detail() SessionDetail {
	return SessionDetail{
		SessionMeta:     s.Meta(),
		MaxIterations:   s.MaxIterations(),
		ModelFallback:   s.ModelFallback(),
		MissingProfiles: s.MissingProfiles(),
		Channels:        s.Channels(),
		Subagents:       s.Subagents(),
	}
}
