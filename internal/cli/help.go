package cli

import "fmt"

// printHelp prints the top-level usage text.
func printHelp() {
	fmt.Println(`harness — fast terminal agent for coding & conversation

Usage:
  harness                            Interactive TUI mode
  harness -p <prompt> [flags]        Single-turn CLI
  harness serve <addr> [--scheduler] Start the HTTP/SSE server (headless transport)
  harness telegram [flags]           Run as a Telegram bot (one session per chat)
  harness telegram pair <chat_id>    Allow a chat to use the bot
  harness telegram unpair <chat_id>  Revoke a chat (also drops its session)
  harness telegram list              List paired chats
  harness --resume <id> [flags]      Resume session in TUI

Management:
  harness providers                  List providers
  harness connect <name> [api_key]   Connect provider (api_key optional)
  harness disconnect <name>          Disconnect provider
  harness sessions [--all]           List sessions (CWD, or all)
  harness delete <id>                Delete session

Settings:
  harness settings                   Show core settings
  harness settings set <key> <val>   Set: key ∈ {model, thinking}
  harness mcp [list]                 List MCP servers
  harness mcp add <name> [flags]     Add MCP server (see 'mcp add' flags)
  harness mcp rm <name>              Remove MCP server
  harness mcp enable <name>          Enable a server
  harness mcp disable <name>         Disable a server (keeps its config)

Memory (read-only — the agent writes memories via its tools):
  harness memo                       List memories (this project + globals)
  harness memo <query>               Full-text search memories
  harness memo <query> --all         Search across ALL projects
  harness memo --global              List only global (cross-project) memories

Schedules (read-only — the agent creates them via its tools):
  harness schedules [--json]         List cron-scheduled prompts (slug, cron, runs, last run)

Flags (CLI / TUI):
  -p, --prompt <text>  Prompt for single-turn CLI mode
  --model <m>          Model (provider/model)
  --thinking <lvl>     Thinking: off|low|medium|high|xhigh
  --output <mode>      With -p: text|json|json-stream
  --resume <id>        Resume session (TUI only)
  --scheduler          Run the cron scheduler engine in the TUI (fires scheduled prompts)
  --all                With sessions: list all
  --help, -h           Show this help

Flags ('mcp add'):
  --command <cmd>      Local server: command + args, e.g. "npx -y @mcp/fs"
  --url <url>          Remote server: server URL
  --bearer <token>     Remote: sugar for --header "Authorization: Bearer <token>"
  --env KEY=VAL        Local: env var (repeatable)
  --header KEY:VAL     Remote: HTTP header (repeatable)
  --disabled           Add the server disabled (default: enabled)
  (transport is inferred: --command → local, --url → remote)

Flags ('memo'):
  --all                Include memories from ALL projects (not just this one)
  --global             Only global (cross-project) memories
  --content            Show each memory's content preview
  --limit <n>          Max results per page (default 10)
  --skip <n>           Pagination offset (default 0)

Flags ('telegram'):
  --token <token>      Bot token (or set TELEGRAM_BOT_TOKEN)
  --model <m>          Model override (provider/model)
  --thinking <lvl>     Thinking level override
  --scheduler          Run the cron scheduler engine (schedules fire to their chat)
  --allow-unpair       Accept any chat, auto-pairing it on first contact
                       (default: only chats paired via 'telegram pair')

Examples:
  harness -p "what is 2+2?"
  harness -p "list files" --output json
  harness -p "hello" --model claude-oauth/opus
  harness --resume abc123 --thinking high
  harness providers
  harness connect anthropic
  harness settings set thinking high
  harness mcp add fs --command "npx -y @mcp/fs"
  harness mcp add api --url https://mcp.x --header "Authorization: Bearer t"
  harness mcp disable everything
  harness memo
  harness memo "deploy process" --content
  harness memo kubernetes --all
  harness serve :8080
  harness telegram pair 456789
  harness telegram --token 123:ABC --scheduler`)
}
