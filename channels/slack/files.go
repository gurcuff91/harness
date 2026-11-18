package slack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// NormalizeXoxD returns the d (xoxd) cookie in the URL-encoded form Slack
// expects. Idempotent (an already-encoded value is left as-is) and tolerant of
// a pasted "d=" prefix and surrounding whitespace/quotes.
func NormalizeXoxD(xoxd string) string {
	v := strings.TrimSpace(xoxd)
	v = strings.TrimPrefix(v, "d=")
	v = strings.Trim(v, `"'`)
	if v == "" || strings.Contains(v, "%") {
		return v
	}
	return url.QueryEscape(v)
}

// fetchFiles downloads the files of an incoming message: images come back as
// prompt images, text files as temp paths (each becomes a <slack-attach/>).
// Other kinds are skipped. Failures are skipped too, never fatal.
func fetchFiles(ctx context.Context, bot *Bot, files []SlackFile) (images []types.ImageData, paths []string) {
	for _, f := range files {
		file := f
		if file.URLPrivate == "" && file.ID != "" {
			info, err := bot.FilesInfo(ctx, file.ID)
			if err != nil {
				continue
			}
			file = *info
		}
		if file.URLPrivate == "" {
			continue
		}
		switch classifyFile(file) {
		case fileKindImage:
			data, err := bot.DownloadFile(ctx, file.URLPrivate)
			if err != nil {
				continue
			}
			mime := file.MimeType
			if mime == "" {
				mime = "image/jpeg"
			}
			images = append(images, types.ImageData{MimeType: mime, Base64: base64.StdEncoding.EncodeToString(data)})
		case fileKindText:
			if p, err := downloadToTemp(ctx, bot, file); err == nil {
				paths = append(paths, p)
			}
		}
	}
	return images, paths
}

// SlackFile is one file attached to a Slack message.
type SlackFile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MimeType   string `json:"mimetype"`
	URLPrivate string `json:"url_private"`
}

// filesInfoResponse is the response from files.info.
type filesInfoResponse struct {
	OK   bool      `json:"ok"`
	File SlackFile `json:"file"`
}

// fileKind classifies a file for routing.
type fileKind int

const (
	fileKindImage  fileKind = iota // send via SendPromptWithImages
	fileKindText                   // download to a temp file, sent as <slack-attach path="..."/>
	fileKindIgnore                 // TODO: PDF, video, audio, binary — future analysis tools
)

// isTextMIME reports whether a mimetype should be treated as plain text
// and downloaded to /tmp for the agent to read via its Read tool.
func isTextMIME(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json",
		"application/xml",
		"application/javascript",
		"application/typescript",
		"application/x-yaml",
		"application/toml",
		"application/x-sh":
		return true
	}
	return false
}

// classifyFile determines how to handle a file based on its mimetype,
// falling back to the file extension when the mimetype is empty or generic.
func classifyFile(f SlackFile) fileKind {
	mime := f.MimeType
	// Fallback by extension when mimetype is missing or too generic.
	if mime == "" || mime == "application/octet-stream" {
		mime = mimeByExtension(filepath.Ext(f.Name))
	}
	if strings.HasPrefix(mime, "image/") {
		return fileKindImage
	}
	if isTextMIME(mime) {
		return fileKindText
	}
	// TODO: handle PDF, video, audio, and other binary types via future analysis tools.
	return fileKindIgnore
}

// mimeByExtension returns a best-guess mimetype for a file extension.
func mimeByExtension(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "text/x-go"
	case ".py":
		return "text/x-python"
	case ".js", ".mjs":
		return "text/javascript"
	case ".ts", ".tsx":
		return "text/typescript"
	case ".java":
		return "text/x-java-source"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/x-yaml"
	case ".toml":
		return "application/toml"
	case ".xml":
		return "application/xml"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".sh", ".bash":
		return "text/x-sh"
	case ".rb":
		return "text/x-ruby"
	case ".rs":
		return "text/x-rust"
	case ".c", ".h":
		return "text/x-c"
	case ".cpp", ".cc", ".cxx":
		return "text/x-c++"
	case ".md", ".txt", ".csv":
		return "text/plain"
	case ".swift":
		return "text/x-swift"
	case ".kt", ".kts":
		return "text/x-kotlin"
	}
	return ""
}

// downloadToTemp downloads a text file to a unique OS temp path using
// os.CreateTemp for safe atomic creation. The pattern is "*-originalname"
// so the resulting path ends with the original filename — the agent's Read
// tool and the model can infer language/context from it (e.g. …-script.py).
func downloadToTemp(ctx context.Context, bot *Bot, f SlackFile) (string, error) {
	data, err := bot.DownloadFile(ctx, f.URLPrivate)
	if err != nil {
		return "", err
	}

	name := filepath.Base(f.Name)
	if name == "" || name == "." {
		name = f.ID + ".txt"
	}

	// Pattern "*-name" → /tmp/1234567890-script.py
	// The OS picks the unique prefix; the suffix is always the original name.
	tmp, err := os.CreateTemp("", "*-"+name)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}
	return tmp.Name(), nil
}

// ── Bot helpers ───────────────────────────────────────────────────────────

// FilesInfo calls files.info to get the full file object (including url_private)
// when it wasn't included in the RTM event.
func (b *Bot) FilesInfo(ctx context.Context, fileID string) (*SlackFile, error) {
	data, err := b.apiCall(ctx, "files.info", map[string]string{"file": fileID})
	if err != nil {
		return nil, err
	}
	var r filesInfoResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("slack files.info: decode: %w", err)
	}
	if !r.OK {
		return nil, fmt.Errorf("slack files.info: api error")
	}
	return &r.File, nil
}

// DownloadFile fetches a Slack private file URL using the session credentials.
// Slack private URLs require Authorization + Cookie headers — they are not
// publicly accessible even with the file_id.
func (b *Bot) DownloadFile(ctx context.Context, urlPrivate string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", urlPrivate, nil)
	if err != nil {
		return nil, fmt.Errorf("slack download: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.xoxc)
	req.Header.Set("Cookie", "d="+b.xoxd)

	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack download: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack download: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
