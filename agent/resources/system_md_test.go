package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSystemMD writes <root>/.harness/agent/SYSTEM.md with the given content.
func writeSystemMD(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "agent")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SYSTEM.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// Project-local SYSTEM.md, no global one — the project's content is used.
func TestSystemMD_ProjectOnlyIsUsed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	writeSystemMD(t, cwd, "project rules")

	res, err := NewFileResourceLoader(cwd).Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.SystemMD != "project rules" {
		t.Errorf("SystemMD = %q, want %q", res.SystemMD, "project rules")
	}
}

// Global SYSTEM.md only, no project-local one — falls back to global.
func TestSystemMD_FallsBackToGlobalWhenNoProjectOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	writeSystemMD(t, home, "global rules")

	res, err := NewFileResourceLoader(cwd).Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.SystemMD != "global rules" {
		t.Errorf("SystemMD = %q, want %q", res.SystemMD, "global rules")
	}
}

// Both exist — the project-local one REPLACES the global one entirely (no
// merge/concatenation); the global content must not appear anywhere.
func TestSystemMD_ProjectTakesPrecedenceOverGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	writeSystemMD(t, home, "global rules")
	writeSystemMD(t, cwd, "project rules")

	res, err := NewFileResourceLoader(cwd).Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.SystemMD != "project rules" {
		t.Errorf("SystemMD = %q, want the project-local content to win entirely", res.SystemMD)
	}
	if strings.Contains(res.SystemMD, "global rules") {
		t.Errorf("SystemMD contains the global content — must be a full replacement, not a merge: %q", res.SystemMD)
	}
}

// Neither exists — SystemMD is empty, no error (same no-op-safe behavior as
// before the project-local override existed).
func TestSystemMD_EmptyWhenNeitherExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	res, err := NewFileResourceLoader(cwd).Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.SystemMD != "" {
		t.Errorf("SystemMD = %q, want empty when neither file exists", res.SystemMD)
	}
}
