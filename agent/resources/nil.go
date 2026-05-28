package resources

// ── NilLoader ───────────────────────────────────────────────────────────

// NilLoader returns empty Resources. Use for tests or SDK minimal mode.
type NilLoader struct{}

func (NilLoader) Load(cwd string) (*Resources, error) {
	return &Resources{}, nil
}
