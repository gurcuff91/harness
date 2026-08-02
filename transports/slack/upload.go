package slack

import (
	"context"
	"strings"
)

const (
	uploadTagOpen  = "<slack:uploadFile>"
	uploadTagClose = "</slack:uploadFile>"
)

// extractUploads parses <slack:uploadFile>/path</slack:uploadFile> tags from
// text, returns the list of file paths and the cleaned text with all tags
// removed (trimmed). Mirrors the Telegram transport's extractUploads.
func extractUploads(text string) (paths []string, clean string) {
	var b strings.Builder
	for {
		open := strings.Index(text, uploadTagOpen)
		if open < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:open])
		rest := text[open+len(uploadTagOpen):]
		close := strings.Index(rest, uploadTagClose)
		if close < 0 {
			// Malformed — no closing tag, write remainder as-is.
			b.WriteString(uploadTagOpen)
			b.WriteString(rest)
			break
		}
		path := strings.TrimSpace(rest[:close])
		if path != "" {
			paths = append(paths, path)
		}
		text = rest[close+len(uploadTagClose):]
	}
	clean = strings.TrimSpace(b.String())
	return paths, clean
}

// sendWithUploads extracts <slack:uploadFile> tags from text, strips them from
// the visible message, and uploads each file to Slack. If there are files, they
// are shared with the cleaned text as initial_comment (one API call per file).
// If there is no text and no files, nothing is sent.
// reason is the SSE event that triggered this send (e.g. "text_end",
// "tool_call", "turn_end") — included in the reply log for observability.
func (t *Transport) sendWithUploads(ctx context.Context, channelID, text, reason string) {
	paths, clean := extractUploads(text)

	if len(paths) == 0 {
		// No uploads — plain text message, goes through send() which logs reply.
		if clean != "" {
			t.sendLogged(ctx, channelID, clean, reason)
		}
		return
	}

	// Convert text once for use as initial_comment on the first upload.
	mrkdwnClean := toMrkdwn(clean)

	// Log the reply (text + files) so it appears in the logs like a normal
	// reply, matching the observability of send().
	kv := []any{"channel", channelID}
	t.mu.Lock()
	if p := t.pumps[channelID]; p != nil && p.model != "" {
		kv = append(kv, "model", p.model)
	}
	t.mu.Unlock()
	if mrkdwnClean != "" {
		kv = append(kv, "text", oneLine(mrkdwnClean, 200))
	}
	kv = append(kv, "files", len(paths))
	if reason != "" {
		kv = append(kv, "trigger", reason)
	}
	t.logger.Info("slack", "reply", kv...)

	// Upload each file. The first file carries the accompanying text as
	// initial_comment; subsequent files share silently (comment already shown).
	for i, path := range paths {
		comment := ""
		if i == 0 {
			comment = mrkdwnClean
		}
		t.logger.Info("slack", "upload_file", "channel", channelID,
			"path", path, "index", i+1, "total", len(paths))
		if err := t.bot.UploadFile(ctx, channelID, path, comment); err != nil {
			t.logger.Error("slack", "upload_file", "channel", channelID,
				"path", path, "error", err.Error())
			// Fall back: send the text and an error notice.
			if i == 0 && mrkdwnClean != "" {
				t.send(ctx, channelID, mrkdwnClean)
			}
			t.send(ctx, channelID, "⚠️ Couldn't upload file: "+path)
		}
	}
}
