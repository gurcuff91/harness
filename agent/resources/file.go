package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── FileResourceLoader ───────────────────────────────────────────────────

// FileResourceLoader discovers context from the filesystem:
//   - SYSTEM.md:  ~/.harness/agent/SYSTEM.md (global only)
//   - AGENTS.md:  <cwd>/AGENTS.md walking up to maxDepth levels
//   - Skills:     loaded from 4 directories in precedence order:
//     1. ~/.agents/skills/              (global system — lowest prio)
//     2. ~/.harness/agent/skills/        (global user)
//     3. <cwd>/.agents/skills/           (local system)
//     4. <cwd>/.harness/agent/skills/    (local project — highest prio)
//
// Skills with the same name: higher priority directory wins.
// All SKILL.md content is eagerly loaded at Load() time.
type FileResourceLoader struct {
	cwd      string
	maxDepth int
	index    map[string]skillEntry // populated by Load(), used by ReadSkill()
}

// NewFileResourceLoader creates a loader for the given working directory.
func NewFileResourceLoader(cwd string) *FileResourceLoader {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return &FileResourceLoader{
		cwd:      cwd,
		maxDepth: 3,
		index:    map[string]skillEntry{},
	}
}

// skillEntry holds a skill's metadata and eagerly loaded content.
type skillEntry struct {
	info    SkillInfo
	content string
}

// Load discovers all resources for the session.
// After Load() returns, ReadSkill() serves from the in-memory index.
func (f *FileResourceLoader) Load() (*Resources, error) {
	home, _ := os.UserHomeDir()

	res := &Resources{}

	// 1. SYSTEM.md — global only
	res.SystemMD = readFileIfExists(
		filepath.Join(home, ".harness", "agent", "SYSTEM.md"),
	)

	// 2. AGENTS.md — walk up from cwd
	res.AgentsMD = f.findAgentsMD()

	// 3. Skills — merge in precedence order (last wins)
	skillDirs := []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".harness", "agent", "skills"),
		filepath.Join(f.cwd, ".agents", "skills"),
		filepath.Join(f.cwd, ".harness", "agent", "skills"),
	}

	f.index = map[string]skillEntry{}
	for _, dir := range skillDirs {
		for name, entry := range loadSkillsFrom(dir) {
			f.index[name] = entry // higher priority dirs overwrite lower
		}
	}

	for _, entry := range f.index {
		res.Skills = append(res.Skills, entry.info)
	}

	return res, nil
}

// ReadSkill returns the eagerly loaded content of a skill by name.
// Must be called after Load().
func (f *FileResourceLoader) ReadSkill(name string) (content string, dir string, err error) {
	entry, ok := f.index[name]
	if !ok {
		return "", "", fmt.Errorf("skill %q not found", name)
	}
	// entry.info.Location is the absolute path to the skill's SKILL.md; the
	// directory that contains it is the skill's base for relative references.
	return entry.content, filepath.Dir(entry.info.Location), nil
}

// ── Internal helpers ─────────────────────────────────────────────────────

// findAgentsMD walks up from cwd looking for AGENTS.md, up to maxDepth levels.
func (f *FileResourceLoader) findAgentsMD() string {
	dir := f.cwd
	for i := 0; i <= f.maxDepth; i++ {
		path := filepath.Join(dir, "AGENTS.md")
		if content := readFileIfExists(path); content != "" {
			return content
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		dir = parent
	}
	return ""
}

// loadSkillsFrom reads all skills from a directory.
// Each skill must be a subdirectory containing SKILL.md.
// Returns nil if the directory doesn't exist.
func loadSkillsFrom(dir string) map[string]skillEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	result := map[string]skillEntry{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(dir, e.Name(), "SKILL.md")
		content := readFileIfExists(skillFile)
		if content == "" {
			continue
		}
		name := e.Name()
		result[name] = skillEntry{
			info: SkillInfo{
				Name:        name,
				Description: extractDescription(content),
				Location:    skillFile,
			},
			content: stripFrontmatter(content),
		}
	}
	return result
}

// extractDescription extracts the description from SKILL.md.
// Tries frontmatter "description:" field first (single-line and multi-line "|"),
// then falls back to the first non-empty line of the body.
func extractDescription(content string) string {
	if strings.HasPrefix(content, "---") {
		end := strings.Index(content[3:], "---")
		if end >= 0 {
			fm := content[3 : end+3]
			lines := strings.Split(fm, "\n")

			// Single-line: description: "some text"
			for _, line := range lines {
				t := strings.TrimSpace(line)
				if strings.HasPrefix(t, "description:") {
					desc := strings.TrimSpace(strings.TrimPrefix(t, "description:"))
					desc = strings.Trim(desc, `"'`)
					// skip block scalar markers
					if desc != "" && desc != "|" && desc != ">" && desc != ">-" && desc != "|-" {
						return desc
					}
					break
				}
			}

			// Multi-line: description: |
			//               line 1
			//               line 2
			inDesc := false
			var descLines []string
			for _, line := range lines {
				t := strings.TrimSpace(line)
				if strings.HasPrefix(t, "description:") {
					inDesc = true
					continue
				}
				if inDesc {
					if t == "" || (len(line) > 0 && line[0] != ' ' && line[0] != '\t') {
						break
					}
					descLines = append(descLines, t)
				}
			}
			if len(descLines) > 0 {
				return strings.Join(descLines, " ")
			}
		}
	}

	// Fallback: first non-empty line of the body
	body := stripFrontmatter(content)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// stripFrontmatter removes YAML frontmatter (--- ... ---) from content.
func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---") {
		return content
	}
	rest := content[3:]
	end := strings.Index(rest, "---")
	if end < 0 {
		return content
	}
	return strings.TrimSpace(rest[end+3:])
}

// readFileIfExists reads a file and returns its content, or "" if not found.
func readFileIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
