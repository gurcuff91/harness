package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gurcuff91/harness/llm"
)

type fetchInput struct {
	URL        string            `json:"url"`
	Method     string            `json:"method,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	OutputPath string            `json:"output_path,omitempty"`
}

// Fetch returns a tool that makes HTTP requests using Go's native http client.
func Fetch() Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "URL to fetch"
			},
			"method": {
				"type": "string",
				"description": "HTTP method (default: GET)"
			},
			"headers": {
				"type": "object",
				"description": "Optional HTTP headers"
			},
			"body": {
				"type": "string",
				"description": "Optional request body (for POST/PUT)"
			},
			"output_path": {
				"type": "string",
				"description": "If provided, write the response bytes directly to this file path (binary-safe). Use for images, PDFs, ZIPs, or any non-text content. Parent directories are created automatically."
			}
		},
		"required": ["url"]
	}`)

	return Tool{
		Def: llm.ToolDef{
			Name:        "fetch",
			Description: "Fetch a URL over HTTP. Provide output_path to save binary content (images, PDFs, ZIPs, etc.) directly to disk — binary-safe, no corruption. Without output_path, returns response body as text (best for JSON, HTML, APIs). Supports GET, POST, PUT, DELETE with optional headers and body.",
			InputSchema: schema,
		},
		Execute: func(input json.RawMessage) (string, error) {
			var args fetchInput
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("parse input: %w", err)
			}

			method := strings.ToUpper(args.Method)
			if method == "" {
				method = "GET"
			}

			// Build request
			var bodyReader io.Reader
			if args.Body != "" {
				bodyReader = strings.NewReader(args.Body)
			}

			req, err := http.NewRequest(method, args.URL, bodyReader)
			if err != nil {
				return fmt.Sprintf("ERROR: %v", err), err
			}

			for k, v := range args.Headers {
				req.Header.Set(k, v)
			}

			// Execute with timeout
			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Sprintf("ERROR: %v", err), err
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Sprintf("ERROR reading body: %v", err), err
			}

			// Binary mode: write raw bytes to disk
			if args.OutputPath != "" {
				path := args.OutputPath
				if strings.HasPrefix(path, "~/") {
					home, _ := os.UserHomeDir()
					path = home + path[1:]
				}
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					return fmt.Sprintf("ERROR creating dirs: %v", err), err
				}
				if err := os.WriteFile(path, body, 0644); err != nil {
					return fmt.Sprintf("ERROR writing file: %v", err), err
				}
				return fmt.Sprintf("HTTP %d\nSaved %d bytes to %s", resp.StatusCode, len(body), path), nil
			}

			// Text mode: return body as string
			result := string(body)
			const maxOutput = 15000
			if len(result) > maxOutput {
				result = result[:maxOutput] + "\n...(truncated)"
			}
			return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, result), nil
		},
	}
}
