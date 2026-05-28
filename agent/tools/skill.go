package tools

import (
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// Skill returns a tool that loads the full content of a skill by name.
// readFn is typically ResourceLoader.ReadSkill — injected by the agent at session creation.
func Skill(readFn func(name string) (string, error)) (types.ToolDef, func(json.RawMessage) (string, error)) {
	def := types.ToolDef{
		Name:        "skill",
		Description: "Read the full instructions for a skill by name. Use this to load a skill before executing it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "Name of the skill to load"}
			},
			"required": ["name"]
		}`),
	}

	execute := func(input json.RawMessage) (string, error) {
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(input, &params); err != nil {
			return fmt.Sprintf("Error parsing input: %v", err), err
		}
		content, err := readFn(params.Name)
		if err != nil {
			return fmt.Sprintf("Error reading skill %q: %v", params.Name, err), err
		}
		return content, nil
	}

	return def, execute
}
