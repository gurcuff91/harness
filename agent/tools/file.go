package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// imageExtToMime maps supported image extensions to MIME types.
var imageExtToMime = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

func isImagePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := imageExtToMime[ext]
	return ok
}

type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func ReadFile() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Read",
			Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp) — images are sent as attachments. For text files, use offset and limit to read specific line ranges. Always prefer this over bash cat/head/tail for reading file content.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path":   {"type": "string",  "description": "Path to the file to read"},
					"offset": {"type": "integer", "description": "Line offset to start reading from (0-based)"},
					"limit":  {"type": "integer", "description": "Maximum number of lines to read"}
				},
				"required": ["path"]
			}`),
		},
		ExecuteRich: func(ctx context.Context, input json.RawMessage) (string, []types.ImageData, error) {
			var args readFileInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), nil, err
			}

			// Image file — return as ImageData
			if isImagePath(args.Path) {
				ext := strings.ToLower(filepath.Ext(args.Path))
				mime := imageExtToMime[ext]
				data, err := os.ReadFile(args.Path)
				if err != nil {
					return fmt.Sprintf("Error reading image: %v", err), nil, err
				}
				img := types.ImageData{
					MimeType: mime,
					Base64:   base64.StdEncoding.EncodeToString(data),
				}
				return fmt.Sprintf("Image loaded: %s (%s, %d bytes)", args.Path, mime, len(data)), []types.ImageData{img}, nil
			}

			// Text file
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return fmt.Sprintf("Error reading file: %v", err), nil, err
			}
			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)
			if args.Offset > 0 {
				if args.Offset >= totalLines {
					return fmt.Sprintf("Offset %d beyond end of file (%d lines total)", args.Offset, totalLines), nil, nil
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
			return content, nil, nil
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
			Description: "Create or overwrite a file with the given content. WARNING: replaces the entire file — use edit for partial changes. Creates parent directories if needed.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to write the file"},
					"content": {"type": "string", "description": "Content to write to the file"}
				},
				"required": ["path", "content"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
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
