package resources

// ── Nil ─────────────────────────────────────────────────────────────────

// nilLoader discovers nothing.
type nilLoader struct{}

// Nil returns a ResourceLoader that discovers nothing: no SYSTEM.md, no
// AGENTS.md, no skills. It is what a nil AgentOptions.ResourceLoader means; pass
// it when you want to say so explicitly.
func Nil() ResourceLoader { return nilLoader{} }

func (nilLoader) Load(string) (*Resources, error) { return &Resources{}, nil }
