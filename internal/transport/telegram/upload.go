package telegram

import (
	"context"
	"github.com/gurcuff91/harness/internal/logx"
	"path/filepath"
	"regexp"
	"strings"
)

// uploadTagRe matches a <tel:uploadFile>path</tel:uploadFile> action tag. The
// path is captured; whitespace around it is trimmed later.
var uploadTagRe = regexp.MustCompile(`(?s)<tel:uploadFile>(.*?)</tel:uploadFile>`)

// extractUploads pulls the file paths from all upload tags in text and returns
// them along with the text stripped of those tags. Stripping always happens —
// even for malformed/empty tags — so nothing leaks to the user. Leftover blank
// lines from removed tags are collapsed.
func extractUploads(text string) (paths []string, cleaned string) {
	for _, m := range uploadTagRe.FindAllStringSubmatch(text, -1) {
		if p := strings.TrimSpace(m[1]); p != "" {
			paths = append(paths, p)
		}
	}
	cleaned = uploadTagRe.ReplaceAllString(text, "")
	cleaned = collapseBlankLines(cleaned)
	return paths, strings.TrimSpace(cleaned)
}

// collapseBlankLines squeezes 3+ consecutive newlines (left by removed tags)
// down to two, keeping paragraph spacing tidy.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// photoExts are sent inline via sendPhoto — the formats Telegram renders as a
// photo. GIF is NOT here: sendPhoto would deliver it as a single static frame,
// so it goes through sendAnimation instead (animExts). Everything else (BMP,
// SVG, PDF, ZIP, …) is sent as a document.
var photoExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
}

// animExts are sent via sendAnimation so they play (animated GIF / silent MP4).
var animExts = map[string]bool{
	".gif": true, ".mp4": true,
}

// sendUploads uploads each path to the chat: images inline (sendPhoto), other
// files as documents (sendDocument). Failures are logged and skipped — a bad
// path never blocks the rest, and the user already got the cleaned text.
func (t *Transport) sendUploads(ctx context.Context, chatID int64, paths []string) {
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		var err error
		switch {
		case photoExts[ext]:
			err = t.bot.SendPhotoFile(ctx, chatID, p)
		case animExts[ext]:
			err = t.bot.SendAnimationFile(ctx, chatID, p)
		default:
			err = t.bot.SendDocumentFile(ctx, chatID, p)
		}
		if err != nil {
			logx.Error("telegram", "upload", "chat", chatID, "file", filepath.Base(p), "error", err.Error())
		} else {
			logx.Info("telegram", "upload", "chat", chatID, "file", filepath.Base(p))
		}
	}
}
