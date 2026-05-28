// Package resources defines how agents discover context: AGENTS.md, SYSTEM.md and skills.
package resources

// ── Types ───────────────────────────────────────────────────────────────

// Resources holds all discovered context loaded by a ResourceLoader.
type Resources struct {
	SystemMD string      // content of SYSTEM.md — concatenated to the base system prompt
	AgentsMD string      // content of AGENTS.md — project context
	Skills   []SkillInfo // discovered skills (lightweight refs — content loaded lazily)
}

// SkillInfo is a lightweight reference to a skill.
// The full content is loaded lazily via ResourceLoader.ReadSkill().
type SkillInfo struct {
	Name        string // skill name e.g. "developer"
	Description string // one-line summary shown in system prompt listing
	Location    string // absolute path to the skill file (SKILL.md or skill.md)
}

// ── Interface ───────────────────────────────────────────────────────────

// ResourceLoader discovers context and reads skill content.
// Each implementation receives its config in its own constructor (New*).
// Load() takes no parameters — config is set at construction time.
type ResourceLoader interface {
	// Load discovers SYSTEM.md, AGENTS.md and available skills.
	Load() (*Resources, error)

	// ReadSkill returns the full content of a skill by name.
	// Returns an error if the skill is not found or cannot be read.
	ReadSkill(name string) (string, error)
}
