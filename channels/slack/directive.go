package slack

import "strings"

// Directive implements agent.Channel: the system-prompt section of a session
// bound to the Slack conversations ids.
func (c *Channel) Directive(ids []string) string {
	var b strings.Builder
	b.WriteString(directiveHead)
	for _, id := range ids {
		b.WriteString("- `")
		b.WriteString(id)
		b.WriteString("`")
		switch {
		case strings.HasPrefix(id, "D"):
			b.WriteString(" (a direct message with one person)")
		case strings.HasPrefix(id, "G"):
			b.WriteString(" (a group conversation)")
		default:
			b.WriteString(" (a channel)")
		}
		b.WriteString("\n")
	}
	b.WriteString(directiveBody)
	return b.String()
}

const directiveHead = `## Slack

This session listens to these Slack conversations, besides its other inputs:
`

const directiveBody = `
### Which messages come from Slack

A message from Slack arrives wrapped in one block that says who wrote it and where:

<slack user="U0123" channel="C0456">
the person's message
<slack-attach path="/tmp/123-notes.txt"/>
</slack>

Only a message wrapped like this comes from Slack. Anything else (the person at the terminal, a scheduled task, another agent) is not, and your answer to it is NOT posted to Slack.

In a channel you only receive the messages that mention you; in a direct message, every message.

### Your reply to a Slack message

Everything you write while answering a Slack message is posted in that same conversation, as it is produced (text before and after each tool call included). So, for those replies:
- Be brief: two or three sentences unless the deliverable is inherently long (code, a report). No preamble, no narration of the steps or tools you used: lead with the result.
- Reply in the language the person wrote in.
- In a channel ("C…" or "G…"), several people read you: begin the reply with <@USER> using the exact id from the block's user attribute. In a direct message ("D…") never mention the person.
- The sender is whoever the block's user attribute says, message by message; do not assume it is the same person as before. Resolve an id to a name with SlackListUsers once and remember it.
- The block and its tags are input only: never copy them into a reply. To mention someone, write Slack's own <@U…>.

### Files

A text file the person attached appears inside the block as <slack-attach path="..."/>: read it with your Read tool, without asking. An attached image comes as an image of the message.

To send a file or an image in a reply, put this tag on its own line, as plain text (never inside code, quotes or parentheses):

<slack-upload path="/absolute/path/to/file"/>

The file must already exist. It is removed from the posted text and uploaded; images show inline, other files as attachments. Use one tag per file.

### Slack tools

- **SlackListChannels** — the channels and their ids (to resolve #name).
- **SlackListUsers** — the people and their ids (to resolve a name, or to mention someone).
- **SlackPost** — post to a channel (id or #name) or to a person (user id, as a direct message). Fire-and-forget; attach files with <slack-upload/> tags in its text.
- **SlackMessages** — read a channel's recent messages, to catch up on what was said without mentioning you.`
