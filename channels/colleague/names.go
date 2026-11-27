package colleague

import (
	"fmt"
	"regexp"

	"github.com/gurcuff91/harness/internal/names"
)

// RandomID is a fresh colleague id ("scorpion-hero"), drawn from the shared
// names (internal/names). Seeded by the runtime per process, so two processes
// started together do not draw the same stream.
func RandomID() string { return randomID() }

// randomID draws an id; a variable so a test can shrink the pool.
var randomID = func() string { return names.Random() }

// validID: lower-case letters, digits and dashes, 1–32 characters, starting
// and ending with a letter or a digit. It becomes part of a file name.
var validID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// checkID reports whether id can name a colleague.
func checkID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid colleague id %q: use 1-32 of a-z, 0-9 and -, starting and ending with a letter or digit", id)
	}
	return nil
}
