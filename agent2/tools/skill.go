// Package tools holds the ReadSkill tool for session injection.
package tools

import (
	"github.com/gurcuff91/harness/types"
	"encoding/json"
	"os"

	"github.com/gurcuff91/harness/agent2/resources"
)

// ReadSkill returns the tool definition and execute function for loading skill files.
// The caller (Agent.NewSession) registers it into the session's tool registry.
func ReadSkill(r *resources.Resources) (types.ToolDef, func(json.RawMessage) (string, error)) {
	var skillList string
	for _, s := range r.Skills {
		skillList += "- **" + s.Name + "**: " + s.Description + "\n"
	}

	def := types.ToolDef{
		Name: "read_skill",
		Description: "Load the full instructions for a skill by name. " +
			"Use this before executing any skill-based workflow. " +
			"Available skills:\n" + skillList,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Name of the skill to load"
				}
			},
			"required": ["name"]
		}`),
	}

	execute := func(input json.RawMessage) (string, error) {
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(input, &params); err != nil {
			return "Error: invalid input. Provide: {\"name\": \"skill-name\"}", nil
		}
		for _, s := range r.Skills {
			if s.Name == params.Name {
				content, err := os.ReadFile(s.Location)
				if err != nil {
					return "Error reading skill file: " + err.Error(), nil
				}
				return string(content), nil
			}
		}
		return "Skill not found: " + params.Name + ". Available: " + skillNames(r.Skills), nil
	}

	return def, execute
}

func skillNames(skills []resources.SkillInfo) string {
	var names string
	for i, s := range skills {
		if i > 0 {
			names += ", "
		}
		names += s.Name
	}
	return names
}
