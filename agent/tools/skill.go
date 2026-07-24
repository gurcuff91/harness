package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// Skill returns a Tool that loads the full content of a skill by name. readFn is
// typically ResourceLoader.ReadSkill — injected by the agent at session creation;
// it returns the skill content and the absolute directory the skill lives in.
func Skill(readFn func(name string) (content string, dir string, err error)) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSkill,
			Description: "Load the full content of a skill by name. The result begins with the skill's absolute directory — any relative paths it references are relative to this directory, so resolve them against it.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Name of the skill to load"}},"required":["name"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			content, dir, err := readFn(params.Name)
			if err != nil {
				return "", fmt.Errorf("skill %q: %w", params.Name, err)
			}
			// Prepend a contextual note with the skill's base directory so the model
			// can resolve any relative paths inside the skill. Truncate HEAD — the
			// important guidance is at the top of a skill, not the end.
			header := fmt.Sprintf("This skill is located at %s\nAny relative paths it references are relative to this directory.\n\n", dir)
			return header + ApplyTruncation("skill", content, true), nil
		},
	}
}
