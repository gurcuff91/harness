package server

import "github.com/gurcuff91/harness/agent/profiles"

// Profile is the agent's profile type (see agent/profiles): a named bundle of
// per-session directives and tools. Kept as an alias so a frontend that
// registers one on the server needs only this package.
type Profile = profiles.Profile

// RegisterProfile registers (or replaces) the profile name on the server's
// agent. Sessions bound to it — open now or opened later, by any client —
// pick it up: the active ones at their next turn boundary.
func (s *Server) RegisterProfile(name string, p Profile) error {
	return s.agent.RegisterProfile(name, p)
}

// UnregisterProfile removes the profile name. Bound sessions keep the name in
// their meta (so it applies again once re-registered) but lose its
// directives/tools.
func (s *Server) UnregisterProfile(name string) { s.agent.UnregisterProfile(name) }

// ProfileNames returns the currently registered profile names, sorted.
func (s *Server) ProfileNames() []string { return s.agent.ProfileNames() }
