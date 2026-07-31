package cli

import "github.com/gurcuff91/harness/client"

// newClient is a thin alias so call sites in this package (which predate the
// unified client package) don't need touching beyond their occasional
// method-name drift — every command in this package still just does
// `c := newClient(addr)`.
func newClient(addr string) *client.Client {
	return client.New(addr)
}
