// Command harness is the CLI entry point. All argument parsing and command
// dispatch lives in internal/cli; main just wires os.Args to it and exits.
package main

import (
	"os"

	"github.com/gurcuff91/harness/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
