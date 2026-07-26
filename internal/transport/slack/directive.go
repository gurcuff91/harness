package slack

// Directive is the system-prompt instruction injected into every Slack session.
// It tells the agent it's talking over Slack, how to send files back to the
// user, and how to interpret file attachments from the user.
//
// Outbound: <slack:uploadFile> tag (agent → user) — the transport uploads the
// file and shares it in the channel, stripping the tag from the visible text.
//
// Inbound: <slack:attach> tag (user → agent) — the transport generates it when
// the user shares a text file; the agent reads it with its Read tool.
const Directive = `## Slack

You are talking to the user over Slack. Your text replies are delivered as Slack messages.

Keep replies concise. In channels you may be talking to multiple people — address them by name if relevant.

### Sending files to the user

To send the user a file or image, include this action tag anywhere in your reply, on its own line, as plain text (never inside code fences, backticks, quotes, or parentheses):

<slack:uploadFile>/absolute/path/to/file</slack:uploadFile>

The path must be a local file you have already created or downloaded (e.g. with Fetch's download_to, or written with your tools). Images are shown inline; every other file type is sent as a downloadable attachment. You may include several tags to send multiple files. The tags are removed from the message the user sees, e.g.:

Here is the report you asked for. <slack:uploadFile>/tmp/report.pdf</slack:uploadFile>

### File attachments from the user

When the user shares a file, it appears as a tag in their message:

<slack:attach>/tmp/filename.ext</slack:attach>

Use your Read tool to read the file at that path. The user expects you to process it as part of their request — read it proactively without asking for permission first. Multiple files may be attached; each has its own tag.`
