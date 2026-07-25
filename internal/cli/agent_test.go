package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gurcuff91/harness/agent"
)

// TestNewOneShotAgentDoesNotPersistToDisk is the regression test for `harness
// -p <prompt>` littering ~/.harness/agent/sessions/ with one-off sessions
// nobody can ever resume (there's no `-p --resume`). newOneShotAgent must use
// an in-memory store instead of agent.New's default FileStore, so creating
// and using a session through it never touches disk.
func TestNewOneShotAgentDoesNotPersistToDisk(t *testing.T) {
	// Isolate HOME so this test can assert "nothing was written" against a
	// clean, disposable directory — agent.New's default FileStore path
	// (~/.harness/agent/sessions/) is derived from os.UserHomeDir(), which
	// reads $HOME on Unix.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	a := newOneShotAgent()
	defer a.Close()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	sess, err := a.NewSession(cwd, resolveAnyModelOrSkip(t, a))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("session Close: %v", err)
	}

	sessionsDir := filepath.Join(tmpHome, ".harness", "agent", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return // never even created the directory — the strongest possible pass
		}
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("newOneShotAgent wrote to disk under an isolated $HOME: %v", entries)
	}
}

// resolveAnyModelOrSkip returns some "provider/model" string agent.NewSession
// will accept, or skips the test if no active provider is configured in this
// environment. NewSession validates the provider/model exist and are active,
// which this test doesn't otherwise care about — only that Store is
// in-memory.
func resolveAnyModelOrSkip(t *testing.T, a *agent.Agent) string {
	t.Helper()
	models := a.Models()
	if len(models) == 0 {
		t.Skip("no active provider configured in this environment — skipping (covered indirectly by newOneShotAgent's Store wiring, which this test's setup already exercises up to NewSession)")
	}
	return models[0].Model
}
