package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// Skill returns a Tool that loads the full content of a skill by name.
// readFn is typically ResourceLoader.ReadSkill — injected by the agent at session creation.
func Skill(readFn func(name string) (string, error)) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSkill,
			Description: "Read the full instructions for a skill by name. Use this to load a skill before executing it.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Name of the skill to load"}},"required":["name"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("skill: invalid input: %w", err)
			}
			content, err := readFn(params.Name)
			if err != nil {
				return "", fmt.Errorf("skill %q: %w", params.Name, err)
			}
			return content, nil
		},
	}
}
