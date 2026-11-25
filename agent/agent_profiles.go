package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gurcuff91/harness/agent/profiles"
	"github.com/gurcuff91/harness/agent/tools"
)

// Profiles — the agent's side (package profiles has the types). The registry
// is the agent's; a session stores the NAMES it is bound to in its meta
// (store.SessionMeta.Profiles); whenever a session is opened, reconfigured or
// forked, resolveProfiles turns those names into the directives and tools they
// add right now.

// RegisterProfile adds or replaces the profile name. Sessions already bound to
// it pick it up at their next turn boundary (an idle one at once).
func (a *Agent) RegisterProfile(name string, p profiles.Profile) error {
	if err := a.profiles.Register(name, p); err != nil {
		return err
	}
	a.reconfigureBound(name)
	return nil
}

// UnregisterProfile removes the profile name. Sessions bound to it keep the
// name (it applies again once it is registered) but lose its directives and
// tools at their next turn boundary, and report it in MissingProfiles.
func (a *Agent) UnregisterProfile(name string) {
	a.profiles.Unregister(name)
	a.reconfigureBound(name)
}

// ProfileNames returns the profiles registered now, sorted.
func (a *Agent) ProfileNames() []string { return a.profiles.Names() }

// resolveProfiles turns a session's bound names into what they add: their
// directives and tools (those registered now) and the names that are not
// registered (missing).
func (a *Agent) resolveProfiles(names []string) (directives []string, ts []tools.Tool, missing []string) {
	return a.profiles.Resolve(names)
}

// checkProfileNames fails when a name the CALLER asked to bind is not
// registered — creating (or rebinding) a session on a profile that does not
// exist must not look like it worked.
func (a *Agent) checkProfileNames(names []string) error {
	var unknown []string
	for _, n := range names {
		if _, ok := a.profiles.Get(n); !ok {
			unknown = append(unknown, fmt.Sprintf("%q", n))
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%w: %s", ErrUnknownProfile, strings.Join(unknown, ", "))
	}
	return nil
}

// ErrUnknownProfile: a session was asked to bind a profile that is not
// registered (the API answers 400).
var ErrUnknownProfile = fmt.Errorf("unknown profile")

// profilesFor decides the profile names a session ends up with, given the
// names it has (stored) and the session options: not passed → the stored ones;
// passed → exactly the names given (validated). It also returns the config with
// the profiles' directives and tools added, ready for the rest of the opening.
func (a *Agent) profilesFor(stored []string, cfg sessionConfig) (names []string, out sessionConfig, missing []string, err error) {
	if cfg.err != nil {
		return nil, cfg, nil, cfg.err
	}
	names = append([]string(nil), stored...)
	if cfg.setProfile {
		if err := a.checkProfileNames(cfg.profiles); err != nil {
			return nil, cfg, nil, err
		}
		names = append([]string(nil), cfg.profiles...)
	}
	directives, ts, missing := a.resolveProfiles(names)
	// The profiles' directives and tools come BEFORE the caller's own, so a
	// session-specific directive or tool still wins.
	cfg.directives = append(append([]string(nil), directives...), cfg.directives...)
	cfg.tools = append(append([]tools.Tool(nil), ts...), cfg.tools...)
	return names, cfg, missing, nil
}

// reconfigureBound re-applies profiles to every session ACTIVE in this agent
// that is bound to profile name (after it was registered or unregistered).
func (a *Agent) reconfigureBound(name string) {
	a.sessMu.Lock()
	var ids []string
	for id, s := range a.activeSessions {
		if slices.Contains(s.store.Meta().Profiles, name) {
			ids = append(ids, id)
		}
	}
	a.sessMu.Unlock()
	for _, id := range ids {
		_ = a.ReconfigureSession(id)
	}
}

// MissingProfiles are the profiles this session is bound to that were not
// registered when it was opened or last reconfigured: they are skipped (the
// session works without them) but their names are kept in the session.
func (s *Session) MissingProfiles() []string {
	v, _ := s.missingProfiles.Load().([]string)
	return append([]string(nil), v...)
}
