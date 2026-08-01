// Shared helpers used across kong_run*.go — not command-specific, so they
// don't belong in any single kong_run_*.go file.
package cli

import "fmt"

// errf is a small fmt.Errorf alias for command Run() methods.
func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }
