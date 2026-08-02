package slack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

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
	fileKindText                   // download to /tmp, send path via <slack:attach>
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

// handleFiles processes the files attached to a Slack message and returns:
//   - images:     ready for SendPromptWithImages
//   - attachTags: <slack:attach>/tmp/file</slack:attach> strings for text files
//
// Files that cannot be classified (PDF, video, audio, etc.) are silently
// ignored with a TODO comment — they will be handled by future analysis tools.
func (t *Transport) handleFiles(ctx context.Context, channelID string, files []SlackFile) (images []types.ImageData, attachTags []string) {
	for _, f := range files {
		// Resolve url_private — call files.info if not already present.
		file := f
		if file.URLPrivate == "" && file.ID != "" {
			if info, err := t.bot.FilesInfo(ctx, file.ID); err == nil {
				file = *info
			} else {
				t.logger.Error("slack", "files_info", "channel", channelID,
					"file", file.ID, "error", err.Error())
				continue
			}
		}
		if file.URLPrivate == "" {
			continue
		}

		switch classifyFile(file) {
		case fileKindImage:
			data, err := t.bot.DownloadFile(ctx, file.URLPrivate)
			if err != nil {
				t.logger.Error("slack", "download_image", "channel", channelID,
					"file", file.Name, "error", err.Error())
				continue
			}
			mime := file.MimeType
			if mime == "" {
				mime = "image/jpeg"
			}
			images = append(images, types.ImageData{
				MimeType: mime,
				Base64:   base64.StdEncoding.EncodeToString(data),
			})
			t.logger.Info("slack", "image_attached", "channel", channelID, "file", file.Name)

		case fileKindText:
			tmpPath, err := downloadToTemp(ctx, t.bot, file)
			if err != nil {
				t.logger.Error("slack", "download_text", "channel", channelID,
					"file", file.Name, "error", err.Error())
				continue
			}
			attachTags = append(attachTags, fmt.Sprintf("<slack:attach>%s</slack:attach>", tmpPath))
			t.logger.Info("slack", "text_attached", "channel", channelID,
				"file", file.Name, "path", tmpPath)

		case fileKindIgnore:
			// TODO: handle PDF, video, audio, and other binary types via future
			// analysis tools (e.g. a PDF parser tool, a video transcription tool).
			t.logger.Info("slack", "file_ignored", "channel", channelID,
				"file", file.Name, "mime", file.MimeType)
		}
	}
	return images, attachTags
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

// buildPrompt combines the user's text with any <slack:attach> tags for text
// files. Images are sent separately via SendPromptWithImages so they don't
// appear here.
// buildPrompt assembles the final prompt sent to the agent:
//
//  1. Context tags (channel + user) at the top so the model always knows who
//     is speaking and from where.
//  2. The user's text.
//  3. Any <slack:attach> tags for text files at the bottom.
//
// channelID is empty for DMs (only the user tag is emitted).
func buildPrompt(channelID, userID, text string, attachTags []string) string {
	var b strings.Builder

	// Context tags — always first.
	if channelID != "" {
		fmt.Fprintf(&b, "<slack:channel>%s</slack:channel> ", channelID)
	}
	if userID != "" {
		fmt.Fprintf(&b, "<slack:user>%s</slack:user>", userID)
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}

	// User's text.
	if text != "" {
		b.WriteString(text)
	}

	// Attach tags at the bottom.
	if len(attachTags) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		for _, tag := range attachTags {
			b.WriteString(tag)
			b.WriteByte('\n')
		}
	}

	return strings.TrimRight(b.String(), "\n")
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
