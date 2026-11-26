// Command gen writes rpc/schema.json, the JSON Schema of the RPC protocol.
// Run it from the module root: go run ./rpc/gen
package main

import (
	"fmt"
	"os"

	"github.com/gurcuff91/harness/rpc"
)

func main() {
	b, err := rpc.Schema()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile("rpc/schema.json", b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
