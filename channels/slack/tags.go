package slack

import (
	"html"
	"regexp"
	"strings"
)

// The tag format of a Slack channel (independent of transports/slack's).
//
// Inbound — every prompt is wrapped in ONE block naming who wrote and where
// (channel is always present, DMs included, since a session may listen to
// several conversations), with the text files the person attached inside it:
//
//	<slack user="U0123" channel="C0456">
//	the message
//	<slack-attach path="/tmp/123-report.csv"/>
//	</slack>
//
// Outbound — the model asks for a file to be uploaded with a self-closing tag
// on its own line; it is removed from the posted text:
//
//	<slack-upload path="/abs/path/file.png"/>
//
// Images travel as prompt images, without a tag.

// buildPrompt wraps one incoming message in its <slack> block.
func buildPrompt(userID, channelID, text string, attachPaths []string) string {
	var b strings.Builder
	b.WriteString(`<slack user="`)
	b.WriteString(attr(userID))
	b.WriteString(`" channel="`)
	b.WriteString(attr(channelID))
	b.WriteString(`">`)
	b.WriteByte('\n')
	if t := strings.TrimSpace(text); t != "" {
		b.WriteString(t)
		b.WriteByte('\n')
	}
	for _, p := range attachPaths {
		b.WriteString(`<slack-attach path="`)
		b.WriteString(attr(p))
		b.WriteString(`"/>`)
		b.WriteByte('\n')
	}
	b.WriteString(`</slack>`)
	return b.String()
}

// attr escapes a value for a double-quoted attribute.
func attr(s string) string { return html.EscapeString(s) }

var (
	// uploadTag matches <slack-upload path="..."/> (single or double quotes,
	// optional spaces, "/>" or ">").
	uploadTag = regexp.MustCompile(`<slack-upload\s+path\s*=\s*(?:"([^"]*)"|'([^']*)')\s*/?>`)
	// inboundTag matches the inbound markup the model may echo back: the
	// <slack ...> opener, its closer and <slack-attach .../>. Real Slack markup
	// (<@U…>, <#C…>, <https://…|label>) never starts with "slack".
	inboundTag = regexp.MustCompile(`</?slack(?:\s[^>]*)?>|<slack-attach\b[^>]*/?>`)
)

// extractUploads returns the paths of every <slack-upload/> in text and the
// text without them, also stripping any inbound tag the model echoed (they
// are input only and must never reach Slack).
func extractUploads(text string) (paths []string, clean string) {
	text = uploadTag.ReplaceAllStringFunc(text, func(m string) string {
		sm := uploadTag.FindStringSubmatch(m)
		p := sm[1]
		if p == "" {
			p = sm[2]
		}
		if p = strings.TrimSpace(html.UnescapeString(p)); p != "" {
			paths = append(paths, p)
		}
		return ""
	})
	text = inboundTag.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return paths, strings.TrimSpace(strings.Join(lines, "\n"))
}
