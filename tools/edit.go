package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gurcuff91/harness/llm"
)

type editInput struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// Edit returns a tool that performs find/replace edits on files.
func Edit() Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to the file to edit"
			},
			"old_text": {
				"type": "string",
				"description": "Exact text to find (must match uniquely)"
			},
			"new_text": {
				"type": "string",
				"description": "Replacement text"
			}
		},
		"required": ["path", "old_text", "new_text"]
	}`)

	return Tool{
		Def: llm.ToolDef{
			Name:        "edit",
			Description: "Edit a file by replacing exact text. The old_text must match exactly one location in the file. Use for surgical changes to existing files without rewriting the entire content.",
			InputSchema: schema,
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args editInput
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			// Read current content
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return fmt.Sprintf("ERROR: %v", err), err
			}
			content := string(data)

			// Count occurrences
			count := strings.Count(content, args.OldText)
			if count == 0 {
				return "ERROR: old_text not found in file", fmt.Errorf("not found")
			}
			if count > 1 {
				return fmt.Sprintf("ERROR: old_text found %d times, must be unique", count), fmt.Errorf("ambiguous")
			}

			// Replace
			newContent := strings.Replace(content, args.OldText, args.NewText, 1)

			// Write back
			if err := os.WriteFile(args.Path, []byte(newContent), 0644); err != nil {
				return fmt.Sprintf("ERROR: %v", err), err
			}

			return fmt.Sprintf("Edited %s (%d bytes changed)", args.Path, len(args.NewText)-len(args.OldText)), nil
		},
	}
}
