// Package profiles holds named bundles of directives and tools that sessions
// can be bound to: the way an embedder gives SOME of an agent's sessions an
// extra behavior without touching the others.
//
// A Profile is defined once (agent.AgentOptions.Profiles, or live with
// Agent.RegisterProfile) and a session is bound to it by NAME
// (agent.SessionWithProfiles): the names are stored in the session, and every
// time it is opened — by any frontend — the agent applies the directives and
// tools of whichever of its profiles are registered at that moment.
package profiles

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gurcuff91/harness/agent/tools"
)

// Profile is one named bundle.
type Profile struct {
	// Directives are appended to the system prompt of every session bound to
	// the profile.
	Directives []string
	// Tools, when set, returns the tools added to each bound session. It is
	// called once every time a session is opened, so it can hand out tools
	// bound to the profile owner's live state.
	Tools func() []tools.Tool
}

// Registry is a concurrency-safe set of profiles by name.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Profile
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Profile{}} }

// ValidName reports whether name can name a profile: not blank.
func ValidName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a profile name cannot be blank")
	}
	return nil
}

// Register adds or replaces the profile name.
func (r *Registry) Register(name string, p Profile) error {
	if err := ValidName(name); err != nil {
		return err
	}
	r.mu.Lock()
	r.m[name] = p
	r.mu.Unlock()
	return nil
}

// Unregister removes the profile name (no error when it was not there).
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	delete(r.m, name)
	r.mu.Unlock()
}

// Get returns the profile name.
func (r *Registry) Get(name string) (Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[name]
	return p, ok
}

// Names returns the registered names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Resolve turns bound names into what they add to a session: the directives
// and tools of every name registered now, in the order of names, and the names
// that are NOT registered (missing). Tools is called here, once per name.
func (r *Registry) Resolve(names []string) (directives []string, ts []tools.Tool, missing []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, n := range names {
		p, ok := r.m[n]
		if !ok {
			missing = append(missing, n)
			continue
		}
		directives = append(directives, p.Directives...)
		if p.Tools != nil {
			ts = append(ts, p.Tools()...)
		}
	}
	return directives, ts, missing
}
