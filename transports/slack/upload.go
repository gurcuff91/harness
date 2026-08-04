package slack

import (
	"context"
	"strings"
)

const (
	uploadTagOpen  = "<slack:uploadFile>"
	uploadTagClose = "</slack:uploadFile>"
)

// inboundTags are the <slack:...> tags this transport injects INTO the prompt
// as context for the model (see buildPrompt in files.go, and the directive):
//
//	<slack:channel>C...</slack:channel>   where the message came from
//	<slack:user>U...</slack:user>         who sent it
//	<slack:attach>/tmp/f.ext</slack:attach>  a downloaded text file's path
//
// They are INPUT-only and must never reach Slack. The model does occasionally
// echo them back — the directive asks it to read a user ID out of
// <slack:user> and re-emit it in Slack's own <@U...> mention syntax, and a
// format transformation like that is easy to get wrong (a real field report
// had both the channel and user tags leak verbatim into a channel reply). The
// transport can't rely on the model never slipping, so it strips them on the
// way out.
//
// Deliberately an explicit list, not "anything matching <slack:*> except
// uploadFile": these three are the complete set of inbound tags, and a future
// one should be a conscious addition here rather than silently swept up by a
// pattern. <slack:uploadFile> is NOT in this list — it's an OUTBOUND tag with
// its own handling (extractUploads consumes it and uploads the file).
var inboundTags = []string{"channel", "user", "attach"}

// stripInboundTags removes every inbound context tag (open tag, contents, and
// close tag) from text. Slack's own markup is untouched — in particular a
// real <@U...> mention is left exactly as-is, since that's the correct output
// form the directive asks for.
func stripInboundTags(text string) string {
	for _, name := range inboundTags {
		open, close := "<slack:"+name+">", "</slack:"+name+">"
		for {
			i := strings.Index(text, open)
			if i < 0 {
				break
			}
			rest := text[i+len(open):]
			j := strings.Index(rest, close)
			if j < 0 {
				// Malformed (no closing tag): drop just the opening tag and
				// stop scanning this name — leaving the remainder intact is
				// better than swallowing the rest of the message.
				text = text[:i] + rest
				break
			}
			text = text[:i] + rest[j+len(close):]
		}
	}
	// Collapse whitespace left behind by removed tags (e.g. the space between
	// the channel and user tags, or a now-empty leading line) without
	// disturbing the message's own formatting.
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// extractUploads parses <slack:uploadFile>/path</slack:uploadFile> tags from
// text, returns the list of file paths and the cleaned text with all tags
// removed (trimmed). Mirrors the Telegram transport's extractUploads.
//
// It also strips the INBOUND context tags (see stripInboundTags) the model
// sometimes echoes back into its reply. This is the single funnel every
// outbound path goes through — the pump's streamed replies (pump.go), SlackPost
// and SlackAsk (tools.go) — so doing it here covers all three at once instead
// of relying on each call site to remember.
func extractUploads(text string) (paths []string, clean string) {
	text = stripInboundTags(text)
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
