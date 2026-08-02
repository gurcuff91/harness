package resources

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// writeSkill creates a minimal skill directory (dir/name/SKILL.md) under dir.
func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestFileResourceLoaderCopyIndependentIndex is the regression test for the
// bug Copy() exists to fix: a sub-agent must get its own loader instance,
// not share the parent's — Load() populates FileResourceLoader.index in
// place, which is not goroutine-safe. This verifies a Copy() starts with an
// empty index (not aliasing the original's map) and, after its own Load(),
// has an index fully independent of the original — mutating one's map must
// never be visible through the other.
func TestFileResourceLoaderCopyIndependentIndex(t *testing.T) {
	// Isolate $HOME so this test's skill counts aren't polluted by whatever
	// global skills (~/.agents/skills, ~/.harness/agent/skills) happen to be
	// installed in the real environment running this test.
	t.Setenv("HOME", t.TempDir())

	cwd := t.TempDir()
	skillsDir := filepath.Join(cwd, ".harness", "agent", "skills")
	writeSkill(t, skillsDir, "alpha", "# Alpha skill\ndoes alpha things")

	original := NewFileResourceLoader(cwd)
	if _, err := original.Load(); err != nil {
		t.Fatalf("original.Load(): %v", err)
	}
	if len(original.index) != 1 {
		t.Fatalf("original.index has %d entries, want 1", len(original.index))
	}

	cp := original.Copy()
	fileCp, ok := cp.(*FileResourceLoader)
	if !ok {
		t.Fatalf("Copy() returned %T, want *FileResourceLoader", cp)
	}
	if fileCp == original {
		t.Fatal("Copy() returned the SAME pointer as the original — must be a distinct instance")
	}
	if fileCp.index == nil {
		t.Fatal("Copy()'s index is nil, want an initialized empty map")
	}
	if len(fileCp.index) != 0 {
		t.Errorf("Copy()'s index has %d entries before its own Load(), want 0 (must not inherit the original's populated index)", len(fileCp.index))
	}

	// Add a second skill AFTER copying, then Load() the copy — it must
	// discover both skills independently, proving it re-reads the
	// filesystem with its own config (same cwd) rather than reusing
	// anything from the original's state.
	writeSkill(t, skillsDir, "beta", "# Beta skill\ndoes beta things")
	if _, err := fileCp.Load(); err != nil {
		t.Fatalf("copy.Load(): %v", err)
	}
	if len(fileCp.index) != 2 {
		t.Errorf("copy.index has %d entries after Load(), want 2 (alpha+beta)", len(fileCp.index))
	}
	// The original's index must be UNCHANGED by the copy's Load() — proving
	// the two maps are genuinely separate, not aliased.
	if len(original.index) != 1 {
		t.Errorf("original.index has %d entries after the COPY's Load(), want 1 (unchanged) — the two loaders must not share state", len(original.index))
	}
}

// TestFileResourceLoaderCopyPreservesConfig verifies Copy() carries over the
// same cwd/maxDepth configuration — it's a fresh instance, not a fresh
// configuration.
func TestFileResourceLoaderCopyPreservesConfig(t *testing.T) {
	original := NewFileResourceLoader("/some/project/dir")
	original.maxDepth = 7 // non-default, to prove it's actually copied

	cp := original.Copy().(*FileResourceLoader)
	if cp.cwd != original.cwd {
		t.Errorf("Copy().cwd = %q, want %q", cp.cwd, original.cwd)
	}
	if cp.maxDepth != original.maxDepth {
		t.Errorf("Copy().maxDepth = %d, want %d", cp.maxDepth, original.maxDepth)
	}
}

// TestFileResourceLoaderConcurrentLoadOnCopiesDoesNotRace is the direct
// regression test for the goroutine-safety concern Copy() addresses: N
// independent copies of the same loader calling Load() concurrently (as
// happens when a session and several of its sub-agents build their tool
// registries around the same time) must not race — verified under -race.
func TestFileResourceLoaderConcurrentLoadOnCopiesDoesNotRace(t *testing.T) {
	cwd := t.TempDir()
	writeSkill(t, filepath.Join(cwd, ".harness", "agent", "skills"), "alpha", "# Alpha\nalpha")

	original := NewFileResourceLoader(cwd)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cp := original.Copy()
			if _, err := cp.Load(); err != nil {
				t.Errorf("copy Load(): %v", err)
			}
		}()
	}
	wg.Wait()
}
