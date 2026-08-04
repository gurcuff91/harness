package slack

import (
	"strings"
	"testing"
)

// TestStripInboundTagsRemovesLeakedContextTags is the regression test for a
// reported leak: a channel reply arrived in Slack containing the raw context
// tags this transport injects INTO the prompt —
//
//	<slack:channel>C0AJJG0LL5D</slack:channel> <slack:user>U09TTE266LF</slack:user>
//
// The directive asks the model to read the user ID out of <slack:user> and
// re-emit it in Slack's own <@U...> mention syntax; that format
// transformation is easy to slip on, and nothing in the outbound path caught
// it — extractUploads only knew about <slack:uploadFile>.
func TestStripInboundTagsRemovesLeakedContextTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "the exact reported leak",
			in:   "<slack:channel>C0AJJG0LL5D</slack:channel> <slack:user>U09TTE266LF</slack:user>\nHere is the answer.",
			want: "Here is the answer.",
		},
		{
			name: "channel tag alone",
			in:   "<slack:channel>C123</slack:channel> done",
			want: "done",
		},
		{
			name: "user tag alone",
			in:   "reply <slack:user>U456</slack:user>",
			want: "reply",
		},
		{
			name: "attach tag echoed back",
			in:   "See <slack:attach>/tmp/report.txt</slack:attach> for details",
			want: "See  for details",
		},
		{
			name: "multiple occurrences of the same tag",
			in:   "<slack:user>U1</slack:user> and <slack:user>U2</slack:user> ok",
			want: "and  ok",
		},
		{
			name: "no tags at all is untouched",
			in:   "Just a normal reply.",
			want: "Just a normal reply.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripInboundTags(c.in); got != c.want {
				t.Errorf("stripInboundTags(%q)\n  got  %q\n  want %q", c.in, got, c.want)
			}
		})
	}
}

// TestStripInboundTagsKeepsRealMentions is the counterpart guard: <@U...> is
// Slack's OWN mention markup and the correct output form the directive asks
// for — it must survive untouched. Stripping it would break the "always
// @mention in channels" behavior this transport depends on.
func TestStripInboundTagsKeepsRealMentions(t *testing.T) {
	in := "<@U09TTE266LF> here's the status you asked for."
	got := stripInboundTags(in)
	if !strings.Contains(got, "<@U09TTE266LF>") {
		t.Errorf("a real Slack mention must survive stripping, got %q", got)
	}
}

// TestStripInboundTagsLeavesUploadFileTagAlone verifies the OUTBOUND tag is
// not swept up: extractUploads has to still find it to upload the file. If
// stripInboundTags ate it, attachments would silently stop working.
func TestStripInboundTagsLeavesUploadFileTagAlone(t *testing.T) {
	in := "Report attached. <slack:uploadFile>/tmp/report.pdf</slack:uploadFile>"
	got := stripInboundTags(in)
	if !strings.Contains(got, "<slack:uploadFile>/tmp/report.pdf</slack:uploadFile>") {
		t.Errorf("the outbound uploadFile tag must be left for extractUploads, got %q", got)
	}
}

// TestStripInboundTagsMalformedTagKeepsTheMessage verifies a tag with no
// closing half doesn't swallow the rest of the reply — dropping the opening
// tag and keeping the text is strictly better than losing the message.
func TestStripInboundTagsMalformedTagKeepsTheMessage(t *testing.T) {
	got := stripInboundTags("<slack:user>U123 and here is the important answer")
	if !strings.Contains(got, "important answer") {
		t.Errorf("a malformed tag must not swallow the message, got %q", got)
	}
	if strings.Contains(got, "<slack:user>") {
		t.Errorf("the dangling opening tag should still be removed, got %q", got)
	}
}

// TestExtractUploadsStripsInboundTags verifies the wiring: every outbound path
// (the pump's streamed replies, SlackPost, SlackAsk) funnels through
// extractUploads, so the strip has to happen there — and must not interfere
// with upload extraction in the same pass.
func TestExtractUploadsStripsInboundTags(t *testing.T) {
	in := "<slack:channel>C1</slack:channel> <slack:user>U2</slack:user>\n" +
		"<@U2> report ready. <slack:uploadFile>/tmp/r.pdf</slack:uploadFile>"

	paths, clean := extractUploads(in)

	if len(paths) != 1 || paths[0] != "/tmp/r.pdf" {
		t.Errorf("upload extraction broke: paths = %v", paths)
	}
	for _, leaked := range []string{"<slack:channel>", "<slack:user>", "</slack:channel>", "</slack:user>"} {
		if strings.Contains(clean, leaked) {
			t.Errorf("inbound tag %q leaked into the outbound text: %q", leaked, clean)
		}
	}
	if !strings.Contains(clean, "<@U2>") {
		t.Errorf("the real mention was lost: %q", clean)
	}
	if !strings.Contains(clean, "report ready.") {
		t.Errorf("message body was lost: %q", clean)
	}
}

// TestExtractUploadsOnlyInboundTagsProducesNoMessage verifies the degenerate
// case: if the model's reply was NOTHING but leaked tags, the cleaned text is
// empty so the send paths skip it entirely (sendWithUploads only sends when
// clean != ""), rather than posting a blank message to the channel.
func TestExtractUploadsOnlyInboundTagsProducesNoMessage(t *testing.T) {
	_, clean := extractUploads("<slack:channel>C1</slack:channel> <slack:user>U2</slack:user>")
	if clean != "" {
		t.Errorf("a reply made only of leaked tags should clean to empty, got %q", clean)
	}
}
