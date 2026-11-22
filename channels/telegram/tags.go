package telegram

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// The tag format of the Telegram channel.
//
// Inbound — every prompt is ONE block naming the chat and the sender, with the
// files the person attached inside it (any kind; an image ALSO travels as a
// prompt image, and is marked image="true"). A file that was not brought in
// says so:
//
//	<telegram chat="123" user="@gus">
//	the message
//	<telegram-attach name="report.pdf" path="/tmp/123-report.pdf"/>
//	<telegram-attach name="photo.jpg" path="/tmp/124-photo.jpg" image="true"/>
//	<telegram-attach name="movie.mp4" skipped="too large (over 20 MB)"/>
//	</telegram>
//
// Outbound — the model asks for a file to be sent with a self-closing tag on
// its own line; it is removed from the posted text:
//
//	<telegram-upload path="/abs/path/file.png"/>

// attachment is one file of an incoming message (see the format above).
type attachment struct {
	Name    string
	Path    string
	Skipped string
	Image   bool
}

// buildPrompt wraps one incoming message in its <telegram> block.
func buildPrompt(chatID int64, username, text string, atts []attachment) string {
	var b strings.Builder
	b.WriteString(`<telegram chat="`)
	b.WriteString(strconv.FormatInt(chatID, 10))
	b.WriteString(`" user="@`)
	b.WriteString(attr(username))
	b.WriteString(`">`)
	b.WriteByte('\n')
	if t := strings.TrimSpace(text); t != "" {
		b.WriteString(t)
		b.WriteByte('\n')
	}
	for _, a := range atts {
		b.WriteString(`<telegram-attach name="`)
		b.WriteString(attr(a.Name))
		if a.Path != "" {
			b.WriteString(`" path="`)
			b.WriteString(attr(a.Path))
			if a.Image {
				b.WriteString(`" image="true`)
			}
		} else {
			b.WriteString(`" skipped="`)
			b.WriteString(attr(a.Skipped))
		}
		b.WriteString(`"/>`)
		b.WriteByte('\n')
	}
	b.WriteString(`</telegram>`)
	return b.String()
}

func attr(s string) string { return html.EscapeString(s) }

var (
	// uploadTag matches <telegram-upload path="..."/> (either quotes, "/>" or ">").
	uploadTag = regexp.MustCompile(`<telegram-upload\s+path\s*=\s*(?:"([^"]*)"|'([^']*)')\s*/?>`)
	// inboundTag matches the inbound markup the model may echo back.
	inboundTag = regexp.MustCompile(`</?telegram(?:\s[^>]*)?>|<telegram-attach\b[^>]*/?>`)
)

// extractUploads returns the paths of every <telegram-upload/> in text and the
// text without them, also stripping inbound tags the model echoed.
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
	out := strings.Join(lines, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return paths, strings.TrimSpace(out)
}
