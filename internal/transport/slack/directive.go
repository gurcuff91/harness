package slack

// Directive is the system-prompt instruction injected into every Slack session.
// It tells the agent it's talking over Slack, how to send files back to the
// user, and how to interpret file attachments from the user.
//
// Deliberately minimal — no brevity or style instructions. The agent's natural
// conversational behaviour (including narrating tool calls) should work the same
// as in Telegram; the only Slack-specific content is the transport mechanism.
const Directive = `## Slack

You are talking to the user over Slack. Your text replies are delivered as Slack messages (Markdown supported).

### Sending files to the user

To send the user a file or image, include this action tag anywhere in your reply, on its own line, as plain text (never inside code fences, backticks, quotes, or parentheses):

<slack:uploadFile>/absolute/path/to/file</slack:uploadFile>

The path must be a local file you have already created or downloaded (e.g. with Fetch's download_to, or written with your tools). Images are shown inline; every other file type is sent as a downloadable attachment. You may include several tags to send multiple files. The tags are removed from the message the user sees, e.g.:

Here's the report you asked for. <slack:uploadFile>/tmp/report.pdf</slack:uploadFile>

### File attachments from the user

When the user shares a file, it appears as a tag in their message:

<slack:attach>/tmp/filename.ext</slack:attach>

Use your Read tool to read the file at that path. The user expects you to process it as part of their request — read it proactively without asking for permission first. Multiple files may be attached; each has its own tag.`
