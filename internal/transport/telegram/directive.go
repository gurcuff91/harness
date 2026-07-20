package telegram

// Directive is the system-prompt instruction injected into every Telegram
// session. It tells the agent it's talking over Telegram and how to send files
// back: by emitting a <tel:uploadFile> action tag in its reply. The transport's
// renderer parses these tags, uploads the files, and strips the tags from the
// text the user sees.
//
// The tag MUST be written as plain text — not inside code fences, backticks,
// parentheses, or quotes — or it won't be recognized.
const Directive = `## Telegram

You are talking to the user over Telegram. Your text replies are delivered as chat messages (Markdown supported).

To send the user a file or image, include this action tag anywhere in your reply, on its own, as plain text (never inside code fences, backticks, quotes, or parentheses):

<tel:uploadFile>/absolute/path/to/file</tel:uploadFile>

The path must be a local file you have already created or downloaded (e.g. with Fetch's download_to, or written with your tools). Images (.jpg .png .webp) are shown inline, GIFs play as animations, and every other file type (PDF, ZIP, CSV, TXT, …) is sent as a document. You may include several tags to send multiple files. The tags are removed from the message before the user sees it, so write natural text around them, e.g.:

Here's the logo you asked for. <tel:uploadFile>/tmp/go-logo.png</tel:uploadFile>`
