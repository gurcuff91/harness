package server

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/tools"
)

// Profile is a named bundle of per-session configuration a frontend
// registers on the server — typically a transport's own system-prompt
// directives and tools (Slack's directive + SlackPost & co.). A session is
// bound to profiles by NAME (store.SessionMeta.Profiles, persisted): every
// time it's opened, by ANY client, the server re-applies the directives and
// tools of whichever of its profiles are registered at that moment. A profile
// that isn't registered right now (e.g. its transport is stopped) is skipped
// entirely — its directive would describe tools that don't exist.
type Profile struct {
	// Directives are appended to the bound sessions' system prompt
	// (agent.WithSessionDirectives).
	Directives []string
	// Tools, when set, returns the tools added to each bound session
	// (agent.WithSessionTools). Called once per session open, so it can hand
	// out tools bound to the profile owner's live state.
	Tools func() []tools.Tool
}

// RegisterProfile registers (or replaces) the profile name. Sessions already
// bound to it pick it up the next time they're opened; a bound session that
// is active right now is reconfigured at its next turn boundary.
func (s *Server) RegisterProfile(name string, p Profile) {
	s.profMu.Lock()
	s.profiles[name] = p
	s.profMu.Unlock()
	s.reconfigureBound(name)
}

// UnregisterProfile removes the profile name. Bound sessions keep the name in
// their meta (so it applies again once re-registered) but lose its
// directives/tools: active ones at their next turn boundary.
func (s *Server) UnregisterProfile(name string) {
	s.profMu.Lock()
	delete(s.profiles, name)
	s.profMu.Unlock()
	s.reconfigureBound(name)
}

// ProfileNames returns the currently registered profile names, sorted.
func (s *Server) ProfileNames() []string {
	s.profMu.RLock()
	defer s.profMu.RUnlock()
	names := make([]string, 0, len(s.profiles))
	for n := range s.profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// validateProfiles reports the first requested name that isn't registered.
func (s *Server) validateProfiles(names []string) error {
	s.profMu.RLock()
	defer s.profMu.RUnlock()
	for _, n := range names {
		if _, ok := s.profiles[n]; !ok {
			return fmt.Errorf("unknown profile %q", n)
		}
	}
	return nil
}

// profileOptions turns a session's bound profile names + one-off directives
// into agent session options: the directives and tools of every bound
// profile registered right now (unregistered ones skipped), then extra.
func (s *Server) profileOptions(names, extraDirectives []string) []agent.SessionOption {
	s.profMu.RLock()
	var directives []string
	var ts []tools.Tool
	for _, n := range names {
		p, ok := s.profiles[n]
		if !ok {
			continue
		}
		directives = append(directives, p.Directives...)
		if p.Tools != nil {
			ts = append(ts, p.Tools()...)
		}
	}
	s.profMu.RUnlock()
	directives = append(directives, extraDirectives...)
	return []agent.SessionOption{agent.WithSessionDirectives(directives...), agent.WithSessionTools(ts...)}
}

// mergeProfiles returns current plus any of add not already in it, and
// whether anything was added. Binding is additive: a frontend opening a
// session with its profile never strips another frontend's.
func mergeProfiles(current, add []string) ([]string, bool) {
	out := append([]string(nil), current...)
	changed := false
	for _, n := range add {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
			changed = true
		}
	}
	return out, changed
}

// reconfigureBound re-applies profiles to every session ACTIVE in this server
// that is bound to profile name (after it was registered/unregistered).
func (s *Server) reconfigureBound(name string) {
	s.mu.RLock()
	var ids []string
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	for _, id := range ids {
		bound := s.agent.SessionProfiles(id)
		if slices.Contains(bound, name) {
			_ = s.agent.ReconfigureSession(id, s.profileOptions(bound, nil)...)
		}
	}
}
