// Package resources defines how agents discover context: AGENTS.md and skills.
package resources

// ── Types ───────────────────────────────────────────────────────────────

// Resources holds all discovered context for a working directory.
type Resources struct {
	SystemPrompt string      // global override from ~/.harness/AGENTS.md
	AgentsMD     string      // project AGENTS.md (nearest ancestor, max N levels up)
	Skills       []SkillInfo // discovered skills (name + description + location)
}

// SkillInfo is a lightweight reference to a skill file.
// The full content is loaded lazily by the read_skill tool.
type SkillInfo struct {
	Name        string // skill name
	Description string // one-line summary for system prompt listing
	Location    string // absolute path to the skill file
}

// ── Interface ───────────────────────────────────────────────────────────

// ResourceLoader discovers context for a working directory.
type ResourceLoader interface {
	Load(cwd string) (*Resources, error)
}
