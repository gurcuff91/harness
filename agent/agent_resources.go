package agent

import (
	"fmt"
	"path/filepath"

	"github.com/gurcuff91/harness/agent/resources"
)

// loadResources loads the context of cwd with the agent's ResourceLoader (nil =
// discover nothing). The loader is stateless and shared by every session and
// sub-agent, so there is nothing to copy. Never returns nil Resources.
func (a *Agent) loadResources(cwd string) (*resources.Resources, error) {
	if a.resourceLoader == nil {
		return &resources.Resources{}, nil
	}
	res, err := a.resourceLoader.Load(cwd)
	if err != nil {
		return &resources.Resources{}, err
	}
	if res == nil {
		res = &resources.Resources{}
	}
	return res, nil
}

// skillReader reads a skill's content, and the directory it lives in, from the
// skills a session was opened with. A loader whose skills have no location (not
// on disk) reports an empty directory.
func skillReader(skills []resources.Skill) func(string) (content, dir string, err error) {
	byName := make(map[string]resources.Skill, len(skills))
	for _, s := range skills {
		byName[s.Name] = s
	}
	return func(name string) (string, string, error) {
		sk, ok := byName[name]
		if !ok {
			return "", "", fmt.Errorf("skill %q not found", name)
		}
		dir := ""
		if sk.Location != "" {
			dir = filepath.Dir(sk.Location)
		}
		return sk.Content, dir, nil
	}
}
