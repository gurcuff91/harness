package cli

import "github.com/gurcuff91/harness/internal/client"

// newClient is a thin alias so call sites in this package (which predate
// internal/client) don't need touching beyond their occasional method-name
// drift (see the CHANGELOG entry for the internal/client unification) — every
// command in this package still just does `c := newClient(addr)`.
func newClient(addr string) *client.Client {
	return client.New(addr)
}
