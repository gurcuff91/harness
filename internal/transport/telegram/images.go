package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/types"
)

// albumWindow is how long to wait for more photos of the same media_group_id
// before treating the album as complete. Telegram delivers an album as separate
// messages sharing a media_group_id, with no "album done" signal, so we debounce.
const albumWindow = 1 * time.Second

// albumBuffer collects the photos of an in-flight album (one media_group_id)
// until the window elapses, then fires them as a single multi-image prompt.
type albumBuffer struct {
	chatID  int64
	caption string
	photos  [][]PhotoSize // each entry is one photo's size variants
	timer   *time.Timer
}

// albums accumulates in-flight albums keyed by media_group_id. Guarded by mu.
type albums struct {
	mu      sync.Mutex
	pending map[string]*albumBuffer
}

func newAlbums() *albums { return &albums{pending: map[string]*albumBuffer{}} }

// add buffers one photo message of an album, (re)starting the completion timer.
// When the window elapses without more photos, fire is called with everything
// collected.
func (a *albums) add(groupID string, chatID int64, caption string, photo []PhotoSize, fire func(*albumBuffer)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	buf := a.pending[groupID]
	if buf == nil {
		buf = &albumBuffer{chatID: chatID}
		a.pending[groupID] = buf
	}
	buf.photos = append(buf.photos, photo)
	if caption != "" {
		buf.caption = caption // caption arrives on one of the album's messages
	}
	if buf.timer != nil {
		buf.timer.Stop()
	}
	buf.timer = time.AfterFunc(albumWindow, func() {
		a.mu.Lock()
		delete(a.pending, groupID)
		a.mu.Unlock()
		fire(buf)
	})
}

// handlePhotoMessage routes an incoming photo message. A single photo (no
// media_group_id) is processed immediately; an album is buffered and fired as
// one multi-image prompt when the window closes.
func (t *Transport) handlePhotoMessage(ctx context.Context, msg *Message) {
	chatID := msg.Chat.ID
	if msg.MediaGroupID == "" {
		// Single photo → one prompt with one image.
		t.dispatchImages(ctx, chatID, msg.Caption, [][]PhotoSize{msg.Photo})
		return
	}
	// Album → buffer by group id, fire once the window elapses.
	t.pendingAlbums.add(msg.MediaGroupID, chatID, msg.Caption, msg.Photo, func(buf *albumBuffer) {
		t.dispatchImages(ctx, buf.chatID, buf.caption, buf.photos)
	})
}

// ── Document (text file) handling ─────────────────────────────────────────

// isTextMIME reports whether a mimetype should be treated as plain text and
// downloaded to a temp file for the agent to read via its Read tool.
func isTextMIME(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/javascript",
		"application/typescript", "application/x-yaml", "application/toml",
		"application/x-sh":
		return true
	}
	return false
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

// handleDocument processes a document message. Text files (source code, JSON,
// YAML, etc.) are downloaded to a temp file and injected as a <tel:attach> tag
// in the prompt. Non-text documents (PDF, video, audio, zip, …) are silently
// ignored — TODO: support them via future analysis tools.
func (t *Transport) handleDocument(ctx context.Context, chatID int64, caption string, doc *Document) {
	mime := doc.MimeType
	if mime == "" || mime == "application/octet-stream" {
		mime = mimeByExtension(filepath.Ext(doc.FileName))
	}

	if !isTextMIME(mime) {
		// TODO: handle PDF, video, audio, and other binary types via future
		// analysis tools (e.g. a PDF parser tool, a video transcription tool).
		logx.Info("telegram", "document_ignored",
			"chat", chatID, "file", doc.FileName, "mime", doc.MimeType)
		return
	}

	data, err := t.bot.DownloadDocument(ctx, doc.FileID)
	if err != nil {
		logx.Error("telegram", "download_document", "chat", chatID,
			"file", doc.FileName, "error", err.Error())
		t.reply(ctx, chatID, "⚠️ Couldn't download the file.")
		return
	}

	ext := filepath.Ext(doc.FileName)
	if ext == "" {
		ext = ".txt"
	}
	tmp, err := os.CreateTemp("", "harness-telegram-*"+ext)
	if err != nil {
		logx.Error("telegram", "create_temp", "chat", chatID, "error", err.Error())
		return
	}
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		logx.Error("telegram", "write_temp", "chat", chatID, "error", err.Error())
		return
	}
	tmpPath := tmp.Name()
	logx.Info("telegram", "document_attached",
		"chat", chatID, "file", doc.FileName, "path", tmpPath)

	pump, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}

	var b strings.Builder
	if caption != "" {
		b.WriteString(caption)
		b.WriteString("\n\n")
	}
	b.WriteString(fmt.Sprintf("<tel:attach>%s</tel:attach>", tmpPath))

	if _, err := t.api.SendPrompt(pump.sessionID, b.String()); err != nil {
		t.replyError(ctx, chatID, err)
	}
}

// dispatchImages downloads each photo, encodes it, and sends one prompt carrying
// all images plus the caption. It reuses the chat's session/pump so the reply
// (and typing indicator) flow exactly like a text prompt.
func (t *Transport) dispatchImages(ctx context.Context, chatID int64, caption string, photos [][]PhotoSize) {
	pump, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	var images []types.ImageData
	for _, sizes := range photos {
		data, err := t.bot.DownloadPhoto(ctx, sizes)
		if err != nil {
			logx.Error("telegram", "download_photo", "chat", chatID, "error", err.Error())
			continue
		}
		images = append(images, types.ImageData{
			MimeType: "image/jpeg", // Telegram photos are JPEG
			Base64:   base64.StdEncoding.EncodeToString(data),
		})
	}
	if len(images) == 0 {
		t.reply(ctx, chatID, "⚠️ Couldn't download the image(s).")
		return
	}
	logx.Info("telegram", "images",
		"chat", chatID, "count", len(images), "caption", oneLine(caption, 120))
	if _, err := t.api.SendPromptWithImages(pump.sessionID, caption, images); err != nil {
		t.replyError(ctx, chatID, err)
	}
}
