package resources

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ── File ────────────────────────────────────────────────────────────────

// fileLoader discovers context from the filesystem:
//   - SYSTEM.md:  <cwd>/.harness/agent/SYSTEM.md, falling back to
//     ~/.harness/agent/SYSTEM.md if the project doesn't have one (whichever is
//     found REPLACES the other entirely — see Load()'s comment)
//   - AGENTS.md:  <cwd>/AGENTS.md walking up to maxDepth levels
//   - Skills:     loaded from 4 directories in precedence order:
//     1. ~/.agents/skills/              (global system — lowest prio)
//     2. ~/.harness/agent/skills/        (global user)
//     3. <cwd>/.agents/skills/           (local system)
//     4. <cwd>/.harness/agent/skills/    (local project — highest prio)
//
// Skills with the same name: higher priority directory wins.
// It holds no state: the cwd is Load's argument, so one value serves every
// session.
type fileLoader struct{ maxDepth int }

// File returns the ResourceLoader that discovers context on disk, in the working
// directory each session passes to Load. One instance is shared by all of an
// agent's sessions.
func File() ResourceLoader { return fileLoader{maxDepth: 3} }

// Load discovers all resources for cwd ("" = the process's working directory).
func (f fileLoader) Load(cwd string) (*Resources, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	home, _ := os.UserHomeDir()

	res := &Resources{}

	// 1. SYSTEM.md — project-local takes precedence over global. A project can
	// override the user's default system-prompt addendum entirely by placing
	// its own <cwd>/.harness/agent/SYSTEM.md; if it doesn't have one, the
	// global ~/.harness/agent/SYSTEM.md is used instead; if neither exists,
	// SystemMD is simply empty (no error, no fallback content). This is a
	// REPLACEMENT, not a merge: when the project-local file exists, the global
	// one is ignored entirely, not concatenated with it.
	res.SystemMD = readFileIfExists(filepath.Join(cwd, ".harness", "agent", "SYSTEM.md"))
	if res.SystemMD == "" {
		res.SystemMD = readFileIfExists(filepath.Join(home, ".harness", "agent", "SYSTEM.md"))
	}

	// 2. AGENTS.md — walk up from cwd
	res.AgentsMD = f.findAgentsMD(cwd)

	// 3. Skills — merge in precedence order (last wins)
	skillDirs := []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".harness", "agent", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(cwd, ".harness", "agent", "skills"),
	}
	merged := map[string]Skill{}
	for _, dir := range skillDirs {
		for name, sk := range loadSkillsFrom(dir) {
			merged[name] = sk // higher priority dirs overwrite lower
		}
	}
	names := make([]string, 0, len(merged))
	for n := range merged {
		names = append(names, n)
	}
	sort.Strings(names) // a stable order: the system prompt lists them
	for _, n := range names {
		res.Skills = append(res.Skills, merged[n])
	}
	return res, nil
}

// ── Internal helpers ─────────────────────────────────────────────────────

// findAgentsMD walks up from cwd looking for AGENTS.md, up to maxDepth levels.
func (f fileLoader) findAgentsMD(cwd string) string {
	dir := cwd
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
func loadSkillsFrom(dir string) map[string]Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	result := map[string]Skill{}
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
		result[name] = Skill{
			Name:        name,
			Description: extractDescription(content),
			Location:    skillFile,
			Content:     stripFrontmatter(content),
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
