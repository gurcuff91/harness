package telegram

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// uploadTagRe matches a <tel:uploadFile>path</tel:uploadFile> action tag. The
// path is captured; whitespace around it is trimmed later.
var uploadTagRe = regexp.MustCompile(`(?s)<tel:uploadFile>(.*?)</tel:uploadFile>`)

// extractUploads pulls file paths from upload tags in the NORMAL text and
// returns them plus the text stripped of those tags. Tags that appear inside a
// code span (`…`) or fenced code block (```…```) are left untouched: the
// directive tells the agent to emit real tags as plain text, so a tag inside
// code is an example (it's explaining how to use them), not a request to upload.
// Respecting our own directive avoids trying to send an example's fake path.
func extractUploads(text string) (paths []string, cleaned string) {
	segs := splitByCode(text)
	var out strings.Builder
	for _, seg := range segs {
		if seg.code {
			out.WriteString(seg.text) // verbatim — examples/snippets pass through
			continue
		}
		s := seg.text
		last := 0
		for _, loc := range uploadTagRe.FindAllStringSubmatchIndex(s, -1) {
			start, end := loc[0], loc[1] // whole tag
			pathStart, pathEnd := loc[2], loc[3]
			// A tag immediately wrapped in quotes or parentheses is an example (the
			// directive forbids that for real tags), so leave it verbatim.
			if wrapped(s, start, end) {
				continue
			}
			if p := strings.TrimSpace(s[pathStart:pathEnd]); p != "" {
				paths = append(paths, p)
			}
			out.WriteString(s[last:start]) // text before the tag
			last = end                     // skip the tag itself
		}
		out.WriteString(s[last:])
	}
	return paths, strings.TrimSpace(collapseBlankLines(out.String()))
}

// wrapped reports whether the tag occupying s[start:end] is immediately
// enclosed by a matching quote or parenthesis — "…", '…', or (…) — which the
// directive uses to denote an example rather than a real upload request.
func wrapped(s string, start, end int) bool {
	if start == 0 || end >= len(s) {
		return false
	}
	switch s[start-1] {
	case '"':
		return s[end] == '"'
	case '\'':
		return s[end] == '\''
	case '(':
		return s[end] == ')'
	}
	return false
}

// codeSeg is a slice of the message tagged as code (inside ``` or `) or not.
type codeSeg struct {
	text string
	code bool
}

// splitByCode splits text into alternating normal/code segments, recognizing
// fenced blocks (```) and inline spans (`). Delimiters stay with their code
// segment so the text round-trips exactly when the segments are concatenated.
func splitByCode(text string) []codeSeg {
	var segs []codeSeg
	var cur strings.Builder
	flush := func(code bool) {
		if cur.Len() > 0 {
			segs = append(segs, codeSeg{cur.String(), code})
			cur.Reset()
		}
	}
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], "```") {
			flush(false)
			end := strings.Index(text[i+3:], "```")
			if end < 0 { // unterminated fence — rest is code
				segs = append(segs, codeSeg{text[i:], true})
				return segs
			}
			segs = append(segs, codeSeg{text[i : i+3+end+3], true})
			i += 3 + end + 3
			continue
		}
		if text[i] == '`' {
			flush(false)
			end := strings.IndexByte(text[i+1:], '`')
			if end < 0 { // unterminated span — treat the backtick as normal text
				cur.WriteByte('`')
				i++
				continue
			}
			segs = append(segs, codeSeg{text[i : i+1+end+1], true})
			i += 1 + end + 1
			continue
		}
		cur.WriteByte(text[i])
		i++
	}
	flush(false)
	return segs
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
			// Verify the file is actually an image before sending as photo.
			// The agent may have downloaded an HTML error page (paywall, redirect,
			// hotlink protection) and saved it with a .jpg extension — Telegram
			// would return IMAGE_PROCESS_FAILED. Fall back to sendDocument so the
			// user still receives something and can see what went wrong.
			if isRealImage(p) {
				err = t.bot.SendPhotoFile(ctx, chatID, p)
			} else {
				t.logger.Info("telegram", "upload_fallback", "chat", chatID,
					"file", filepath.Base(p), "reason", "file is not a valid image (likely HTML)")
				err = t.bot.SendDocumentFile(ctx, chatID, p)
			}
		case animExts[ext]:
			err = t.bot.SendAnimationFile(ctx, chatID, p)
		default:
			err = t.bot.SendDocumentFile(ctx, chatID, p)
		}
		if err != nil {
			t.logger.Error("telegram", "upload", "chat", chatID, "file", filepath.Base(p), "error", err.Error())
		} else {
			t.logger.Info("telegram", "upload", "chat", chatID, "file", filepath.Base(p))
		}
	}
}

// isRealImage checks the file's magic bytes to confirm it's actually an image
// (JPEG, PNG, GIF, WebP) rather than an HTML page saved with an image extension.
func isRealImage(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 12)
	n, err := f.Read(header)
	if err != nil || n < 4 {
		return false
	}
	// JPEG: FF D8 FF
	if header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF {
		return true
	}
	// PNG: 89 50 4E 47
	if header[0] == 0x89 && header[1] == 0x50 && header[2] == 0x4E && header[3] == 0x47 {
		return true
	}
	// GIF: 47 49 46 38
	if header[0] == 0x47 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x38 {
		return true
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if n >= 12 && header[0] == 0x52 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x46 &&
		header[8] == 0x57 && header[9] == 0x45 && header[10] == 0x42 && header[11] == 0x50 {
		return true
	}
	return false
}
