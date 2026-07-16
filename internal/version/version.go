// Package version is the single source of truth for the harness version.
// The value is injected at build time via ldflags (see the Makefile):
//
//	-X github.com/gurcuff91/harness/version.Version=$(VERSION)
//
// It falls back to "dev" for plain `go build`/`go run` without the Makefile.
package version

// Version is the harness release, set via ldflags at build time.
var Version = "dev"
