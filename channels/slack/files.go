package slack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// attachment is one non-image file of an incoming message: downloaded to
// Path, or not, with the reason in Skipped (Name says which file it was).
type attachment struct {
	Name    string
	Path    string
	Skipped string
}

// fetchFiles brings in the files of an incoming message: images become prompt
// images, every other file is downloaded as is and handed over by path (the
// model chooses how to read it). Nothing is dropped silently: a file that is
// too large or cannot be downloaded is still listed, with the reason — an
// image too, since it cannot be shown either.
func fetchFiles(ctx context.Context, bot *Bot, files []SlackFile) (images []types.ImageData, atts []attachment) {
	for _, f := range files {
		file := f
		if file.URLPrivate == "" && file.ID != "" {
			if info, err := bot.FilesInfo(ctx, file.ID); err == nil {
				file = *info
			}
		}
		name := file.Name
		if name == "" {
			name = file.ID
		}
		if file.URLPrivate == "" {
			atts = append(atts, attachment{Name: name, Skipped: "not downloadable"})
			continue
		}
		if file.Size > maxAttachmentBytes {
			atts = append(atts, attachment{Name: name, Skipped: tooLargeReason()})
			continue
		}
		if isImage(file) {
			data, err := bot.DownloadFile(ctx, file.URLPrivate)
			if err != nil {
				atts = append(atts, attachment{Name: name, Skipped: skipReason(err)})
				continue
			}
			mime := file.MimeType
			if !strings.HasPrefix(mime, "image/") {
				if mime = imageMIMEByExtension(filepath.Ext(file.Name)); mime == "" {
					mime = "image/jpeg"
				}
			}
			images = append(images, types.ImageData{MimeType: mime, Base64: base64.StdEncoding.EncodeToString(data)})
			continue
		}
		p, err := downloadToTemp(ctx, bot, file)
		if err != nil {
			atts = append(atts, attachment{Name: name, Skipped: skipReason(err)})
			continue
		}
		atts = append(atts, attachment{Name: name, Path: p})
	}
	return images, atts
}

func tooLargeReason() string {
	if maxAttachmentBytes >= 1<<20 && maxAttachmentBytes%(1<<20) == 0 {
		return fmt.Sprintf("too large (over %d MB)", maxAttachmentBytes>>20)
	}
	return fmt.Sprintf("too large (over %d bytes)", maxAttachmentBytes)
}

func skipReason(err error) string {
	if errors.Is(err, errTooLarge) {
		return tooLargeReason()
	}
	return "download failed"
}

// SlackFile is one file attached to a Slack message.
type SlackFile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MimeType   string `json:"mimetype"`
	URLPrivate string `json:"url_private"`
	Size       int64  `json:"size"` // bytes, as Slack reports it (0 = unknown)
}

// filesInfoResponse is the response from files.info.
type filesInfoResponse struct {
	OK   bool      `json:"ok"`
	File SlackFile `json:"file"`
}

// maxAttachmentBytes is the largest file a message may bring in (images
// included). A bigger one is not downloaded: the model is told it was skipped.
// A variable for tests.
var maxAttachmentBytes int64 = 50 << 20

// errTooLarge marks a download stopped at maxAttachmentBytes.
var errTooLarge = errors.New("too large")

// isImage reports whether a file goes to the model as an image of the message.
func isImage(f SlackFile) bool {
	mime := f.MimeType
	if mime == "" || mime == "application/octet-stream" {
		mime = imageMIMEByExtension(filepath.Ext(f.Name))
	}
	return strings.HasPrefix(mime, "image/")
}

// imageMIMEByExtension guesses an image mimetype when Slack gave none.
func imageMIMEByExtension(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// downloadToTemp downloads any file to a unique OS temp path using
// os.CreateTemp for safe atomic creation. The pattern is "*-originalname"
// so the resulting path ends with the original filename — the model can infer
// the format from it (…-script.py, …-report.docx) and pick a tool to read it.
func downloadToTemp(ctx context.Context, bot *Bot, f SlackFile) (string, error) {
	data, err := bot.DownloadFile(ctx, f.URLPrivate)
	if err != nil {
		return "", err
	}

	name := filepath.Base(f.Name)
	if name == "" || name == "." || name == "/" {
		name = f.ID
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
	// Never hold more than the limit, whatever size Slack announced.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("slack download: read: %w", err)
	}
	if int64(len(data)) > maxAttachmentBytes {
		return nil, errTooLarge
	}
	return data, nil
}
