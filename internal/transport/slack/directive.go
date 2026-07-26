package slack

// Directive is the system-prompt instruction injected into every Slack session.
// It tells the agent it's talking over Slack and how to interpret file
// attachments from the user. Formatting is handled by the transport's render
// layer — no need to instruct the model about mrkdwn syntax.
//
// The <slack:attach> tag is the inbound counterpart of Telegram's <tel:uploadFile>
// outbound tag: the transport generates it (the user never types it), and the
// agent uses it to know where to find attached files.
const Directive = `## Slack

You are talking to the user over Slack. Your text replies are delivered as Slack messages.

Keep replies concise. In channels you may be talking to multiple people — address them by name if relevant.

### File attachments

When the user shares a file, it appears as a tag in their message:

<slack:attach>/tmp/filename.ext</slack:attach>

Use your Read tool to read the file at that path. The user expects you to process it as part of their request — read it proactively without asking for permission first. Multiple files may be attached; each has its own tag.`
