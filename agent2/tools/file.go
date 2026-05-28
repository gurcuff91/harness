package tools

import (
	"github.com/gurcuff91/harness/types"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

)

type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"` // line offset, 0-based
	Limit  int    `json:"limit,omitempty"`  // max lines to read
}

// ReadFile returns a tool that reads file contents.
func ReadFile() Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to the file to read"
			},
			"offset": {
				"type": "integer",
				"description": "Line offset to start reading from (0-based)"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of lines to read"
			}
		},
		"required": ["path"]
	}`)

	return Tool{
		Def: types.ToolDef{
			Name:        "read_file",
			Description: "Read the contents of a file. Use offset and limit to read specific line ranges in large files. Always prefer this over bash cat/head/tail for reading file content.",
			InputSchema: schema,
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args readFileInput
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			data, err := os.ReadFile(args.Path)
			if err != nil {
				return fmt.Sprintf("ERROR: %v", err), err
			}

			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)

			// Apply offset
			if args.Offset > 0 {
				if args.Offset >= totalLines {
					return fmt.Sprintf("(offset %d beyond end of file, %d lines total)", args.Offset, totalLines), nil
				}
				lines = lines[args.Offset:]
			}

			// Apply limit
			if args.Limit > 0 && args.Limit < len(lines) {
				lines = lines[:args.Limit]
			}

			content := strings.Join(lines, "\n")

			// Truncate if still too large
			const maxSize = 50000
			if len(content) > maxSize {
				content = content[:maxSize] + "\n...(truncated)"
			}

			// Add context about what was returned
			if args.Offset > 0 || args.Limit > 0 {
				header := fmt.Sprintf("[lines %d-%d of %d]\n", args.Offset+1, args.Offset+len(lines), totalLines)
				content = header + content
			}

			return content, nil
		},
	}
}

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFile returns a tool that writes content to a file.
func WriteFile() Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to write the file"
			},
			"content": {
				"type": "string",
				"description": "Content to write to the file"
			}
		},
		"required": ["path", "content"]
	}`)

	return Tool{
		Def: types.ToolDef{
			Name:        "write_file",
			Description: "Create or overwrite a file with the given content. Creates parent directories if needed. Use for new files or full rewrites. For partial changes, prefer the edit tool instead.",
			InputSchema: schema,
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args writeFileInput
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			// Create parent dirs
			dir := filepath.Dir(args.Path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Sprintf("ERROR creating directory: %v", err), err
			}

			if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
				return fmt.Sprintf("ERROR writing file: %v", err), err
			}

			return fmt.Sprintf("Wrote %d bytes to %s", len(args.Content), args.Path), nil
		},
	}
}
