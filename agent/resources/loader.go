// Package resources defines how agents discover context: AGENTS.md, SYSTEM.md and skills.
package resources

// ── Types ───────────────────────────────────────────────────────────────

// Resources holds all discovered context loaded by a ResourceLoader.
type Resources struct {
	SystemMD string      // content of SYSTEM.md — REPLACES the base system prompt (see buildSystemPrompt), not concatenated to it
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

	// ReadSkill returns the full content of a skill by name plus the absolute
	// directory the skill lives in (so relative paths it references — scripts,
	// templates — can be resolved). Returns an error if the skill is not found or
	// cannot be read.
	ReadSkill(name string) (content string, dir string, err error)

	// Copy returns a fresh, independent instance with the same configuration
	// as the receiver — safe to call Load() on concurrently with the
	// original, or with any other Copy(). Implementations that build
	// per-Load() state (e.g. FileResourceLoader's skill index, populated
	// fresh on every Load()) MUST NOT share that state with the receiver;
	// each copy needs its own.
	//
	// This exists specifically for sub-agents: they need their OWN loader
	// instance (see agent.go's buildSessionTools, where the Subagent tool's
	// executor calls loader.Copy()) rather than sharing the parent
	// session's — but they must still use the SAME underlying
	// implementation and configuration the parent was given, not a
	// hardcoded fallback to FileResourceLoader. Before Copy() existed, the
	// executor hardcoded `resources.NewFileResourceLoader(cwd)` regardless
	// of what loader the parent Agent was actually configured with — a
	// custom ResourceLoader injected via harness.WithResourceLoader (e.g.
	// loading skills from a database or object store) was silently ignored
	// for every sub-agent, which saw a completely different, filesystem-only
	// context than the parent session did.
	Copy() ResourceLoader
}
