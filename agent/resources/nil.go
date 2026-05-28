package resources

import "fmt"

// ── NilLoader ───────────────────────────────────────────────────────────

// NilLoader returns empty Resources and errors on ReadSkill.
// Use for tests or minimal SDK usage where no context discovery is needed.
type NilLoader struct{}

func (NilLoader) Load() (*Resources, error) {
	return &Resources{}, nil
}

func (NilLoader) ReadSkill(name string) (string, error) {
	return "", fmt.Errorf("skill %q not found (NilLoader has no skills)", name)
}
