package telegram

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transports/telegram was removed in v0.116.0 (the bot is this channel now):
// nothing may bring it back by importing it, tests included, and the
// directory itself must stay gone.
func TestNothingImportsTheRemovedTelegramTransport(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "transports", "telegram")); err == nil {
		t.Error("transports/telegram exists again")
	}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() {
			if strings.HasPrefix(rel, ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		af, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range af.Imports {
			if strings.Contains(imp.Path.Value, "/transports/telegram") {
				t.Errorf("%s imports transports/telegram", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
