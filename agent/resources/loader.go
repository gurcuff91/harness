// Package resources defines how agents discover context: AGENTS.md, SYSTEM.md and skills.
package resources

import "encoding/json"

// ── Types ───────────────────────────────────────────────────────────────

// Resources holds all the context a ResourceLoader found for one working
// directory.
type Resources struct {
	SystemMD string  `json:"system_md,omitempty"` // content of SYSTEM.md — REPLACES the base system prompt (see buildSystemPrompt), not concatenated to it
	AgentsMD string  `json:"agents_md,omitempty"` // content of AGENTS.md — project context
	Skills   []Skill `json:"skills"`              // the skills available, each with its content
}

// MarshalJSON always writes skills as an array (a nil slice would be null).
func (r Resources) MarshalJSON() ([]byte, error) {
	type plain Resources // no methods: avoids recursion
	if r.Skills == nil {
		r.Skills = []Skill{}
	}
	return json.Marshal(plain(r))
}

// Skill is one skill: what the system prompt lists (Name, Description) and what
// the Skill tool hands the model when it loads it (Content).
type Skill struct {
	Name        string `json:"name"`        // skill name e.g. "developer"
	Description string `json:"description"` // one-line summary shown in the system prompt listing
	// Location is the absolute path to the skill's file (SKILL.md); the
	// directory that contains it is the base for the relative paths the skill
	// references (scripts, templates). A loader whose skills do not live on a
	// disk may leave it empty; the Skill tool then reports no directory.
	Location string `json:"location,omitempty"`
	// Content is the skill's body, frontmatter removed. It is loaded with the
	// skill, not on demand: Load already reads every skill, and one call that
	// returns everything is what a loader that lives across a process boundary
	// needs.
	Content string `json:"content"`
}

// ── Interface ───────────────────────────────────────────────────────────

// ResourceLoader discovers the context of a working directory. It has NO state
// and is bound to NO directory: every session of an agent (and every sub-agent)
// calls Load with its own cwd, so one instance serves them all and may be
// shared freely across goroutines. An implementation must therefore be safe
// for concurrent use.
//
// AgentOptions.ResourceLoader nil means "discover nothing". To discover on disk,
// pass File().
type ResourceLoader interface {
	// Load discovers SYSTEM.md, AGENTS.md and the skills available for cwd.
	// It returns non-nil Resources unless it fails.
	Load(cwd string) (*Resources, error)
}
