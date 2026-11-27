package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
)

// recordingLoader is a stateless ResourceLoader that records the cwd of every
// Load, and serves one skill per cwd.
type recordingLoader struct {
	mu   *sync.Mutex
	cwds *[]string
}

func (r recordingLoader) Load(cwd string) (*resources.Resources, error) {
	r.mu.Lock()
	*r.cwds = append(*r.cwds, cwd)
	r.mu.Unlock()
	return &resources.Resources{
		AgentsMD: "AGENTS for " + cwd,
		Skills:   []resources.Skill{{Name: "s", Description: "d", Content: "content of " + cwd}},
	}, nil
}

func newRecordingLoader() (recordingLoader, *[]string) {
	var cwds []string
	return recordingLoader{mu: &sync.Mutex{}, cwds: &cwds}, &cwds
}

// Every session hands ITS OWN cwd to the one shared loader: the loader is not
// bound to a directory (that is what the interface change fixed).
func TestEachSessionLoadsItsOwnCwdFromTheSharedLoader(t *testing.T) {
	l, cwds := newRecordingLoader()
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{ResourceLoader: l})

	s1, err := a.NewSession("/proj/one", model)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := a.NewSession("/proj/two", model)
	if err != nil {
		t.Fatal(err)
	}
	if len(*cwds) != 2 || (*cwds)[0] != "/proj/one" || (*cwds)[1] != "/proj/two" {
		t.Fatalf("Load called with %v, want each session's cwd", *cwds)
	}
	for _, c := range []struct {
		s   *Session
		cwd string
	}{{s1, "/proj/one"}, {s2, "/proj/two"}} {
		if got := c.s.SystemPrompt(); !strings.Contains(got, "AGENTS for "+c.cwd) || strings.Contains(got, "AGENTS for "+other(c.cwd)) {
			t.Errorf("%s: prompt does not hold exactly its own AGENTS.md", c.cwd)
		}
		content, _, err := c.s.ReadSkill("s")
		if err != nil || content != "content of "+c.cwd {
			t.Errorf("%s: ReadSkill = %q, %v", c.cwd, content, err)
		}
	}
}

func other(cwd string) string {
	if cwd == "/proj/one" {
		return "/proj/two"
	}
	return "/proj/one"
}

// A resumed session loads again, with the cwd it was saved with.
func TestResumeLoadsTheSavedCwd(t *testing.T) {
	l, cwds := newRecordingLoader()
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{ResourceLoader: l})
	s, err := a.NewSession("/proj/saved", model)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()
	*cwds = nil
	if _, err := a.ResumeSession(id); err != nil {
		t.Fatal(err)
	}
	if len(*cwds) != 1 || (*cwds)[0] != "/proj/saved" {
		t.Fatalf("resume loaded %v, want [/proj/saved]", *cwds)
	}
}

// The default is to discover NOTHING: a nil ResourceLoader never touches the
// disk, however many AGENTS.md or skills the directory has. (It used to mean
// "read the project's files"; the CLI now asks for that with resources.File().)
func TestNilResourceLoaderDiscoversNothing(t *testing.T) {
	cwd := projectWithFiles(t)
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{ResourceLoader: nil})
	s, err := a.NewSession(cwd, model)
	if err != nil {
		t.Fatal(err)
	}
	p := s.SystemPrompt()
	if strings.Contains(p, "SECRET PROJECT RULES") || strings.Contains(p, "OVERRIDE PROMPT") || strings.Contains(p, "Available Skills") {
		t.Fatalf("a nil loader read the project's files:\n%.300s", p)
	}
	if len(s.Skills()) != 0 {
		t.Errorf("skills = %v, want none", s.Skills())
	}
}

// The same directory IS read when resources.File() is asked for.
func TestFileResourceLoaderReadsTheProjectWhenAskedFor(t *testing.T) {
	cwd := projectWithFiles(t)
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{ResourceLoader: resources.File()})
	s, err := a.NewSession(cwd, model)
	if err != nil {
		t.Fatal(err)
	}
	p := s.SystemPrompt()
	if !strings.Contains(p, "SECRET PROJECT RULES") || !strings.Contains(p, "OVERRIDE PROMPT") || !strings.Contains(p, "- x:") {
		t.Fatalf("resources.File() did not read the project's files:\n%.400s", p)
	}
	if content, _, err := s.ReadSkill("x"); err != nil || content != "# X\nx" {
		t.Errorf("ReadSkill = %q, %v", content, err)
	}
}

func projectWithFiles(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"AGENTS.md":                        "SECRET PROJECT RULES",
		".harness/agent/SYSTEM.md":         "OVERRIDE PROMPT",
		".harness/agent/skills/x/SKILL.md": "# X\nx",
	} {
		full := filepath.Join(cwd, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cwd
}

// A sub-agent gets the parent's loader (not a copy, not a filesystem one): the
// loader is stateless, so sharing it is safe, and a custom implementation keeps
// serving sub-agents' context.
func TestSubAgentsShareTheParentsLoader(t *testing.T) {
	l, _ := newRecordingLoader()
	a := New(AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: l})
	defer a.Close()
	if a.resourceLoader != l {
		t.Fatalf("the agent holds %T, want the configured loader itself", a.resourceLoader)
	}
}

// The Skill tool tells the model the directory a skill lives in (relative paths
// in it resolve against it): the directory of Location, or none when the loader's
// skills do not live on a disk.
func TestSkillReaderReportsTheSkillsDirectory(t *testing.T) {
	read := skillReader([]resources.Skill{
		{Name: "disk", Content: "on disk", Location: "/skills/disk/SKILL.md"},
		{Name: "remote", Content: "from a database"},
	})
	if content, dir, err := read("disk"); err != nil || content != "on disk" || dir != "/skills/disk" {
		t.Errorf("disk skill: %q, %q, %v; want the file's directory", content, dir, err)
	}
	if content, dir, err := read("remote"); err != nil || content != "from a database" || dir != "" {
		t.Errorf("remote skill: %q, %q, %v; want no directory", content, dir, err)
	}
	if _, _, err := read("nope"); err == nil {
		t.Error("an unknown skill must be an error")
	}
}
