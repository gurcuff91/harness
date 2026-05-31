package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func ReadFile() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Read",
			Description: "Read the contents of a file. Use offset and limit to read specific line ranges in large files. Always prefer this over bash cat/head/tail for reading file content.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to the file to read"},
					"offset": {"type": "integer", "description": "Line offset to start reading from (0-based)"},
					"limit": {"type": "integer", "description": "Maximum number of lines to read"}
				},
				"required": ["path"]
			}`),
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args readFileInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return fmt.Sprintf("Error reading file: %v", err), err
			}
			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)
			if args.Offset > 0 {
				if args.Offset >= totalLines {
					return fmt.Sprintf("Offset %d beyond end of file (%d lines total)", args.Offset, totalLines), nil
				}
				lines = lines[args.Offset:]
			}
			if args.Limit > 0 && args.Limit < len(lines) {
				lines = lines[:args.Limit]
			}
			content := strings.Join(lines, "\n")
			const maxSize = 50000
			if len(content) > maxSize {
				content = content[:maxSize] + "\n...(truncated)"
			}
			if args.Offset > 0 || args.Limit > 0 {
				content = fmt.Sprintf("[lines %d-%d of %d]\n", args.Offset+1, args.Offset+len(lines), totalLines) + content
			}
			return content, nil
		},
	}
}

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func WriteFile() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Write",
			Description: "Create or overwrite a file with the given content. Creates parent directories if needed. Use for new files or full rewrites. For partial changes, prefer the edit tool instead.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to write the file"},
					"content": {"type": "string", "description": "Content to write to the file"}
				},
				"required": ["path", "content"]
			}`),
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args writeFileInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if err := os.MkdirAll(filepath.Dir(args.Path), 0755); err != nil {
				return fmt.Sprintf("Error creating directory: %v", err), err
			}
			if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
				return fmt.Sprintf("Error writing file: %v", err), err
			}
			return fmt.Sprintf("Wrote %d bytes to %s", len(args.Content), args.Path), nil
		},
	}
}
