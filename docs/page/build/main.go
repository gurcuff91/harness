// Command build turns docs/page/content/**/*.md into a static site.
//
// Standard library only — the harness takes no dependencies, and neither does
// its documentation. Usage:
//
//	go run . -content ../content -static ../static -out ../../../site -version v0.127.0
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	content := flag.String("content", "content", "directory with the Markdown pages")
	static := flag.String("static", "static", "directory with the site's own assets")
	out := flag.String("out", "site", "output directory (replaced)")
	version := flag.String("version", "dev", "harness version shown in the header")
	base := flag.String("base", "", "URL prefix the site is served under (e.g. /harness)")
	flag.Parse()

	site, err := load(*content)
	if err != nil {
		fail(err)
	}
	if err := render(site, *static, *out, *version, *base); err != nil {
		fail(err)
	}
	fmt.Printf("built %d pages → %s\n", len(site.pages), filepath.Clean(*out))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
