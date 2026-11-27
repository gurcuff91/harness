package names

import (
	"regexp"
	"testing"
)

var shape = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)

func TestRandomIsACharacterAndATrait(t *testing.T) {
	for i := 0; i < 500; i++ {
		if n := Random(); !shape.MatchString(n) || len(n) > 32 {
			t.Fatalf("name %q is not <character>-<trait> within 32 characters", n)
		}
	}
}

func TestSizeIsTheProductOfTheLists(t *testing.T) {
	if Size() != len(characters)*len(traits) || Size() < 1000 {
		t.Fatalf("Size() = %d", Size())
	}
}
