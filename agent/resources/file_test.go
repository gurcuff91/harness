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

// One loader serves every directory: the cwd is Load's argument, not part of
// the loader. This is what replaced Copy(): there is no per-session instance.
func TestOneFileLoaderServesDifferentWorkingDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(a, ".harness", "agent", "skills"), "alpha", "# Alpha\nalpha body")
	writeSkill(t, filepath.Join(b, ".harness", "agent", "skills"), "beta", "# Beta\nbeta body")
	writeSystemMD(t, a, "rules of A")

	l := File()
	ra, err := l.Load(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := l.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.Skills) != 1 || ra.Skills[0].Name != "alpha" || ra.SystemMD != "rules of A" {
		t.Errorf("cwd A: skills %v, SYSTEM.md %q", ra.Skills, ra.SystemMD)
	}
	if len(rb.Skills) != 1 || rb.Skills[0].Name != "beta" || rb.SystemMD != "" {
		t.Errorf("cwd B: skills %v, SYSTEM.md %q (must not see A's)", rb.Skills, rb.SystemMD)
	}
}

// A skill carries its content with it (Load reads it), so the Skill tool needs
// nothing from the loader afterwards.
func TestLoadedSkillsCarryTheirContentAndLocation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".harness", "agent", "skills")
	writeSkill(t, dir, "alpha", "---\ndescription: does alpha\n---\n# Alpha\nalpha body")

	res, err := File().Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skills) != 1 {
		t.Fatalf("skills = %v", res.Skills)
	}
	sk := res.Skills[0]
	if sk.Description != "does alpha" || sk.Content != "# Alpha\nalpha body" {
		t.Errorf("skill = %+v (frontmatter must be stripped from Content)", sk)
	}
	if sk.Location != filepath.Join(dir, "alpha", "SKILL.md") {
		t.Errorf("Location = %q", sk.Location)
	}
}

// Skills come out in a stable order (the system prompt lists them).
func TestSkillsAreListedInAStableOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".harness", "agent", "skills")
	for _, n := range []string{"zeta", "alpha", "mid", "beta"} {
		writeSkill(t, dir, n, "# "+n+"\n"+n)
	}
	for i := 0; i < 20; i++ {
		res, _ := File().Load(cwd)
		var got []string
		for _, s := range res.Skills {
			got = append(got, s.Name)
		}
		if want := []string{"alpha", "beta", "mid", "zeta"}; !equalStrings(got, want) {
			t.Fatalf("run %d: skills = %v, want %v", i, got, want)
		}
	}
}

// Higher-priority directories win over lower ones, per skill name.
func TestProjectSkillOverridesGlobalOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	writeSkill(t, filepath.Join(home, ".harness", "agent", "skills"), "same", "# Global\nglobal")
	writeSkill(t, filepath.Join(cwd, ".harness", "agent", "skills"), "same", "# Project\nproject")
	res, _ := File().Load(cwd)
	if len(res.Skills) != 1 || res.Skills[0].Content != "# Project\nproject" {
		t.Fatalf("skills = %+v, want the project's", res.Skills)
	}
}

// The loader has no state, so the SAME instance may be used by many goroutines
// at once (sessions and sub-agents) — the reason Copy() is gone. Run under -race.
func TestConcurrentLoadsOnOneLoaderDoNotRace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	writeSkill(t, filepath.Join(cwd, ".harness", "agent", "skills"), "alpha", "# Alpha\nalpha")
	l := File()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := l.Load(cwd)
			if err != nil || len(res.Skills) != 1 {
				t.Errorf("Load: %v %v", res, err)
			}
		}()
	}
	wg.Wait()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
