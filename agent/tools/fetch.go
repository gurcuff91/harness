package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

type fetchFile struct {
	Field string `json:"field"`
	Path  string `json:"path"`
}

type fetchInput struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Body helpers — mutually exclusive (raw body OR json OR form; files may
	// combine with form for multipart).
	Body  string            `json:"body,omitempty"`  // raw string body
	JSON  json.RawMessage   `json:"json,omitempty"`  // object → JSON body + application/json
	Form  map[string]string `json:"form,omitempty"`  // → x-www-form-urlencoded, or multipart when files present
	Files []fetchFile       `json:"files,omitempty"` // → multipart/form-data (may include form fields)
	// Behavior.
	Timeout         int  `json:"timeout,omitempty"`          // request timeout in seconds (default 30)
	FollowRedirects *bool `json:"follow_redirects,omitempty"` // default true; false returns the 3xx as-is
	// Response destination.
	DownloadTo string `json:"download_to,omitempty"` // save the response bytes to this path (binary-safe)
}

func Fetch() Tool {
	return Tool{
		Def: types.ToolDef{
			Name: "Fetch",
			Description: "Fetch a URL over HTTP for APIs, web pages, and downloads. Prefer this over running curl/wget through Bash — it handles headers, JSON, forms, file uploads, redirects, gzip, and binary downloads correctly, and reports status/errors cleanly. Supports GET, HEAD, POST, PUT, PATCH, DELETE.\n\n" +
				"Body: send a request body one of four ways (choose ONE) — 'body' for a raw string; 'json' for an object (serialized and sent as application/json); 'form' for key/values (sent as application/x-www-form-urlencoded); 'files' to upload files as multipart/form-data. 'files' may be combined with 'form' to include text fields alongside the uploads.\n\n" +
				"Headers: pass 'headers' as an object; the Content-Type for json/form/files is set automatically.\n\n" +
				"Behavior: 'timeout' sets the request timeout in seconds (default 30). Redirects are followed by default; set 'follow_redirects' to false to inspect a 3xx response (e.g. read its Location header) without following it.\n\n" +
				"Download: set 'download_to' to save the raw response bytes to a local path (binary-safe — images, PDFs, ZIPs); parent dirs are created. Without it, the response is returned as text.\n\n" +
				"Response: the result shows the status line, response headers, and body. 4xx/5xx statuses are reported as errors. Text output is truncated to the first 2000 lines or 50KB; when truncated, the full status+headers+body is saved to a temp file whose path is shown.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "URL to fetch"},
					"method": {"type": "string", "description": "HTTP method (default: GET)"},
					"headers": {"type": "object", "description": "Optional HTTP headers"},
					"body": {"type": "string", "description": "Raw string request body. Mutually exclusive with json/form/files."},
					"json": {"type": "object", "description": "Object sent as a JSON body (sets Content-Type: application/json). Mutually exclusive with body/form/files."},
					"form": {"type": "object", "description": "Key/value pairs sent as application/x-www-form-urlencoded, or as multipart fields when 'files' is also given.", "additionalProperties": {"type": "string"}},
					"files": {
						"type": "array",
						"description": "Files to upload as multipart/form-data. May be combined with 'form' for text fields.",
						"items": {
							"type": "object",
							"properties": {
								"field": {"type": "string", "description": "Form field name"},
								"path": {"type": "string", "description": "Local path of the file to upload"}
							},
							"required": ["field", "path"]
						}
					},
					"timeout": {"type": "integer", "description": "Request timeout in seconds (default: 30)."},
					"follow_redirects": {"type": "boolean", "description": "Follow HTTP redirects (default: true). Set false to inspect a 3xx response without following it."},
					"download_to": {"type": "string", "description": "Save the raw response bytes to this local path (binary-safe). Creates parent dirs. Without it, the response is returned as text."}
				},
				"required": ["url"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args fetchInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}

			method := strings.ToUpper(args.Method)
			if method == "" {
				method = "GET"
			}

			// Build the request body from exactly one helper.
			bodyReader, contentType, err := buildFetchBody(&args)
			if err != nil {
				return err.Error(), err
			}

			req, err := http.NewRequestWithContext(ctx, method, args.URL, bodyReader)
			if err != nil {
				return fmt.Sprintf("Error building request: %v", err), err
			}
			for k, v := range args.Headers {
				req.Header.Set(k, v)
			}
			// Auto Content-Type for json/form/files unless the caller set one.
			if contentType != "" && req.Header.Get("Content-Type") == "" {
				req.Header.Set("Content-Type", contentType)
			}

			timeout := 30 * time.Second
			if args.Timeout > 0 {
				timeout = time.Duration(args.Timeout) * time.Second
			}
			client := &http.Client{Timeout: timeout}
			// follow_redirects=false → return the 3xx response as-is (don't follow),
			// so the caller can read its Location header.
			if args.FollowRedirects != nil && !*args.FollowRedirects {
				client.CheckRedirect = func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				}
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), err
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Sprintf("Error reading body: %v", err), err
			}

			// Download mode: write raw bytes to disk.
			if args.DownloadTo != "" {
				// On 4xx/5xx do NOT save — writing the error page as the requested
				// file would be misleading (the agent would think the download
				// succeeded). Report the failure with the response body instead, like
				// `wget` / `curl --fail`.
				if resp.StatusCode >= 400 {
					return formatResponse(resp, string(body)), fmt.Errorf("HTTP %s", resp.Status)
				}
				path := expandHome(args.DownloadTo)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					return fmt.Sprintf("Error creating dirs: %v", err), err
				}
				if err := os.WriteFile(path, body, 0644); err != nil {
					return fmt.Sprintf("Error writing file: %v", err), err
				}
				return fmt.Sprintf("HTTP %s — saved %d bytes to %s", resp.Status, len(body), path), nil
			}

			// Text mode: status line + response headers + body (JS Response style).
			full := formatResponse(resp, string(body))
			result := ApplyTruncation("fetch", full, true)
			if resp.StatusCode >= 400 {
				// Only 4xx/5xx are failures. 2xx is success; 3xx redirects are
				// followed automatically by the client (a 3xx only surfaces here on
				// too many redirects or a missing Location — informational, not an
				// error). Surface 4xx/5xx so the model knows the request failed.
				return result, fmt.Errorf("HTTP %s", resp.Status)
			}
			return result, nil
		},
	}
}

// buildFetchBody resolves the single body helper into a reader + Content-Type.
// Enforces mutual exclusion: body | json | form | files (files may add form
// fields). Returns (nil, "", nil) when there is no body.
func buildFetchBody(args *fetchInput) (io.Reader, string, error) {
	hasBody := args.Body != ""
	hasJSON := len(args.JSON) > 0
	hasForm := len(args.Form) > 0
	hasFiles := len(args.Files) > 0

	// Count exclusive body sources (files+form counts as one multipart source).
	sources := 0
	if hasBody {
		sources++
	}
	if hasJSON {
		sources++
	}
	if hasForm && !hasFiles {
		sources++
	}
	if hasFiles {
		sources++
	}
	if sources > 1 {
		return nil, "", fmt.Errorf("provide only one request body: 'body', 'json', 'form', or 'files' (files may include form fields)")
	}

	switch {
	case hasFiles:
		return buildMultipart(args.Files, args.Form)
	case hasJSON:
		return bytes.NewReader(args.JSON), "application/json", nil
	case hasForm:
		vals := url.Values{}
		for k, v := range args.Form {
			vals.Set(k, v)
		}
		return strings.NewReader(vals.Encode()), "application/x-www-form-urlencoded", nil
	case hasBody:
		return strings.NewReader(args.Body), "", nil
	default:
		return nil, "", nil
	}
}

// buildMultipart assembles a multipart/form-data body from files (read off disk)
// plus optional text fields.
func buildMultipart(files []fetchFile, fields map[string]string) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", fmt.Errorf("multipart field %q: %w", k, err)
		}
	}
	for _, f := range files {
		path := expandHome(f.Path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("reading upload %q: %w", f.Path, err)
		}
		fw, err := w.CreateFormFile(f.Field, filepath.Base(path))
		if err != nil {
			return nil, "", fmt.Errorf("multipart file %q: %w", f.Field, err)
		}
		if _, err := fw.Write(data); err != nil {
			return nil, "", fmt.Errorf("writing upload %q: %w", f.Field, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("finalizing multipart: %w", err)
	}
	return &buf, w.FormDataContentType(), nil
}

// formatResponse renders the response JS-Response-style: status line, response
// headers (sorted), a blank line, then the body.
func formatResponse(resp *http.Response, body string) string {
	var b strings.Builder
	b.WriteString("HTTP " + resp.Status + "\n")
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range resp.Header[k] {
			b.WriteString(k + ": " + v + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
}

// expandHome resolves a leading ~/ to the user's home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}
