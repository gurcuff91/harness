package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gurcuff91/harness/types"
)

type editInput struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func Edit() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "edit",
			Description: "Edit a file by replacing exact text. The old_text must match exactly one location in the file. Use for surgical changes to existing files without rewriting the entire content.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to the file to edit"},
					"old_text": {"type": "string", "description": "Exact text to find (must match uniquely)"},
					"new_text": {"type": "string", "description": "Replacement text"}
				},
				"required": ["path", "old_text", "new_text"]
			}`),
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args editInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return fmt.Sprintf("Error reading file: %v", err), err
			}
			content := string(data)
			count := strings.Count(content, args.OldText)
			if count == 0 {
				err := fmt.Errorf("old_text not found in file")
				return err.Error(), err
			}
			if count > 1 {
				err := fmt.Errorf("old_text found %d times, must be unique", count)
				return err.Error(), err
			}
			newContent := strings.Replace(content, args.OldText, args.NewText, 1)
			if err := os.WriteFile(args.Path, []byte(newContent), 0644); err != nil {
				return fmt.Sprintf("Error writing file: %v", err), err
			}
			return fmt.Sprintf("Edited %s (%+d bytes)", args.Path, len(args.NewText)-len(args.OldText)), nil
		},
	}
}
