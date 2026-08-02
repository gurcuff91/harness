package resources

import "testing"

// TestNilLoaderCopyReturnsNilLoader verifies NilLoader's trivial Copy() —
// it carries no state, so any NilLoader value is interchangeable with any
// other, but Copy() still must satisfy the ResourceLoader interface.
func TestNilLoaderCopyReturnsNilLoader(t *testing.T) {
	var l ResourceLoader = NilLoader{}
	cp := l.Copy()
	if _, ok := cp.(NilLoader); !ok {
		t.Fatalf("Copy() returned %T, want NilLoader", cp)
	}
}
