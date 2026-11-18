package slack

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// channels/slack must stand on its own: nothing in it imports transports/slack,
// so the transport can be removed without touching this package.
func TestDoesNotImportTheTransport(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			if strings.Contains(imp.Path.Value, "/transports/") {
				t.Errorf("%s imports %s", f, imp.Path.Value)
			}
		}
	}
}

// Outside the transport itself, only the `harness slack` bot command (and its
// admin list) may still use it: every other Slack need (login, the input
// channel) is served by this package.
func TestOnlyTheBotCommandUsesTheTransport(t *testing.T) {
	allowed := map[string]bool{
		"internal/cli/kong_run_slack.go": true, // `harness slack` (the RTM bot) and `slack admin`
		"harness.go":                     true, // the SDK's RunSlack alias
	}
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() {
			if rel == "transports" || strings.HasPrefix(rel, ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		af, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range af.Imports {
			if strings.Contains(imp.Path.Value, "/transports/slack") && !allowed[filepath.ToSlash(rel)] {
				t.Errorf("%s imports transports/slack", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
