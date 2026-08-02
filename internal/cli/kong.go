// Package cli — Kong CLI grammar.
//
// This file defines the complete command-line structure using Kong struct
// tags: flags, positional args, enums, defaults, and help text all live here
// in one place. Command types are named (not anonymous) so each one's Run()
// method can be attached separately, grouped by area, in kong_run*.go.
//
// Enum gotcha: an OPTIONAL enum field (empty string = "use the settings
// default", not a fixed value) needs BOTH the empty string as a valid enum
// value AND an explicit default:"" — Kong panics with "enum value is only
// valid if it is either required or has a valid default value" if only one of
// the two is present. Every --thinking flag below follows this pattern.
//
// Positional-arg + subcommand conflict: Kong forbids mixing `arg:""` with
// `cmd:""` in the same struct ("can't mix positional arguments and branching
// arguments"). This is why `slack admin <userID>` is `slack admin add
// <userID>` below, not a bare positional — `add`/`list`/`remove` are genuine
// subcommand siblings.
//
// Single-default-per-level: only one command per tree level may carry
// default:"1"/"withargs".
//
// Parent Run() vs. child subcommands: Kong's Context.Run() calls Run() on
// EVERY node in the selected chain, child to parent, not just the leaf — by
// design, for shared setup logic (see the docker example in Kong's repo).
// That means a command that both DOES something itself (bare `harness
// telegram`, `harness slack`) AND has real subcommands (`telegram list`,
// `slack login`) can't put its own action in that parent struct's Run(): it
// would fire a second time after every subcommand, and the subcommand's
// --help would inherit all the parent's flags too. The fix used throughout
// below is the same one TUI uses at the root: the action lives in its own
// hidden default:"withargs" child (e.g. telegramRunCmd, slackRunCmd), a
// sibling of the real subcommands — never a Run() directly on a struct that
// also declares cmd:"" children.
package cli

// ── Root grammar ─────────────────────────────────────────────────────────

// CLI is the root Kong grammar for harness. TUI is the default command
// (hidden from the top-level command list, but selected when no other
// command is named).
var CLI struct {
	TUI tuiCmd `cmd:"" default:"withargs" hidden:"" help:"Interactive TUI (default)"`

	Serve serveCmd `cmd:"" help:"Start the HTTP/SSE server (headless transport)"`

	Telegram telegramCmd `cmd:"" help:"Run as a Telegram bot (one session per chat)"`

	Slack slackCmd `cmd:"" help:"Run as a Slack user bot (one session per channel/DM)"`

	ACP acpCmd `cmd:"" help:"Run as an Agent Client Protocol agent over stdio (for Zed and other ACP clients)"`

	// ── Management ───────────────────────────────────────────────────────

	Providers providersCmd `cmd:"" help:"List providers"`

	Connect connectCmd `cmd:"" help:"Connect a provider"`

	Disconnect disconnectCmd `cmd:"" help:"Disconnect a provider"`

	Sessions sessionsCmd `cmd:"" help:"List sessions (current directory, or --all)"`

	Delete deleteCmd `cmd:"" help:"Delete a session"`

	// ── Settings ─────────────────────────────────────────────────────────

	Settings settingsCmd `cmd:"" help:"Show or set core settings"`

	MCP mcpCmd `cmd:"" help:"Manage MCP servers"`

	Memo memoCmd `cmd:"" help:"Search/list memories (read-only — the agent writes memories via its tools)"`

	Schedules schedulesCmd `cmd:"" help:"List cron-scheduled prompts (read-only — the agent creates them via its tools)"`
}

// ── TUI / one-shot prompt ────────────────────────────────────────────────

type tuiCmd struct {
	Model     string `help:"Model (provider/model)"`
	Thinking  string `enum:",off,low,medium,high,xhigh" default:"" help:"Thinking level"`
	Resume    string `help:"Resume session by id"`
	Scheduler bool   `help:"Run the cron scheduler engine"`
	Prompt    string `short:"p" help:"Run a single-turn prompt instead of the interactive TUI"`
	Output    string `enum:"text,json,json-stream" default:"text" help:"Output mode (with --prompt/-p)"`
}

// ── serve ────────────────────────────────────────────────────────────────

type serveCmd struct {
	Addr      string `arg:"" help:"Address to listen on (e.g. :8080)"`
	Scheduler bool   `help:"Run the cron scheduler engine"`
}

// ── acp ──────────────────────────────────────────────────────────────────

// acpCmd has no flags: an ACP client (Zed) launches this exact command as a
// sub-process and speaks JSON-RPC to it over stdin/stdout — there is no
// terminal for flags to be typed into, and every per-session setting (model,
// thinking) is negotiated over the protocol itself instead (see
// transports/acp's session config options).
type acpCmd struct{}

// ── telegram ─────────────────────────────────────────────────────────────

// telegramCmd is a pure command group: no flags, no Run() of its own — only
// the flags/action that actually run the bot live in telegramRunCmd (the
// hidden default child), so they don't leak into pair/unpair/list's own
// --help or re-fire after those subcommands run (see the parent-Run() note
// atop this file).
type telegramCmd struct {
	Run    telegramRunCmd    `cmd:"" default:"withargs" hidden:"" help:"Run as a Telegram bot (default)"`
	Pair   telegramPairCmd   `cmd:"" help:"Allow a chat to use the bot"`
	Unpair telegramUnpairCmd `cmd:"" help:"Revoke a chat"`
	List   telegramListCmd   `cmd:"" help:"List paired chats"`
	Token  telegramTokenCmd  `cmd:"" help:"Save the bot token (or check the saved one with --status)"`
}

type telegramRunCmd struct {
	Token       string `env:"TELEGRAM_BOT_TOKEN" help:"Bot token (or set TELEGRAM_BOT_TOKEN)"`
	Model       string `help:"Model override (provider/model)"`
	Thinking    string `enum:",off,low,medium,high,xhigh" default:"" help:"Thinking level override"`
	Scheduler   bool   `help:"Run the cron scheduler engine"`
	AllowUnpair bool   `name:"allow-unpair" help:"Accept any chat, auto-pairing on first contact"`
}

type telegramPairCmd struct {
	ChatID int64 `arg:"" help:"Chat ID to allow"`
}

type telegramUnpairCmd struct {
	ChatID int64 `arg:"" help:"Chat ID to revoke (also drops its session)"`
}

type telegramListCmd struct{}

type telegramTokenCmd struct {
	Token  string `arg:"" optional:"" help:"Bot token to save (omit with --status to check the saved one)"`
	Status bool   `help:"Show whether a token is saved, instead of saving one"`
}

// ── slack ────────────────────────────────────────────────────────────────

// slackCmd is a pure command group: no flags, no Run() of its own — only the
// flags/action that actually run the bot live in slackRunCmd (the hidden
// default child), so they don't leak into login/admin's own --help or
// re-fire after those subcommands run (see the parent-Run() note atop this
// file).
type slackCmd struct {
	Run   slackRunCmd   `cmd:"" default:"withargs" hidden:"" help:"Run as a Slack user bot (default)"`
	Login slackLoginCmd `cmd:"" help:"Authenticate interactively (saves to ~/.harness/slack.json)"`
	Admin slackAdminCmd `cmd:"" help:"Manage Slack admins"`
}

type slackRunCmd struct {
	Workspace string `env:"SLACK_WORKSPACE" help:"Slack workspace URL (or set SLACK_WORKSPACE)"`
	XoxC      string `name:"xoxc" env:"SLACK_XOXC" help:"xoxc- session token (or set SLACK_XOXC)"`
	XoxD      string `name:"xoxd" env:"SLACK_XOXD" help:"xoxd- session cookie (or set SLACK_XOXD)"`
	Model     string `help:"Model override (provider/model)"`
	Thinking  string `enum:",off,low,medium,high,xhigh" default:"" help:"Thinking level override"`
	Scheduler bool   `help:"Run the cron scheduler engine"`
}

type slackLoginCmd struct {
	Status bool `help:"Verify saved credentials instead of logging in"`
}

type slackAdminCmd struct {
	Add    slackAdminAddCmd    `cmd:"" help:"Add a user as admin (can run /reset /stop /compact /thinking /model)"`
	List   slackAdminListCmd   `cmd:"" help:"List current admins"`
	Remove slackAdminRemoveCmd `cmd:"" help:"Remove an admin"`
}

type slackAdminAddCmd struct {
	UserID string `arg:"" help:"User ID to add as admin"`
}

type slackAdminListCmd struct{}

type slackAdminRemoveCmd struct {
	UserID string `arg:"" help:"User ID to remove"`
}

// ── management ───────────────────────────────────────────────────────────

type providersCmd struct{}

type connectCmd struct {
	Name   string `arg:"" help:"Provider name"`
	APIKey string `arg:"" optional:"" help:"API key (optional — omit for OAuth providers)"`
}

type disconnectCmd struct {
	Name string `arg:"" help:"Provider name"`
}

type sessionsCmd struct {
	All bool `help:"List sessions across all directories"`
}

type deleteCmd struct {
	SessionID string `arg:"" help:"Session ID to delete"`
}

// ── settings ─────────────────────────────────────────────────────────────

// settingsCmd is a pure command group: showing current settings on a bare
// `harness settings` lives in settingsShowCmd (the hidden default child), not
// on settingsCmd itself — a Run() there would also fire after `settings set`
// (see the parent-Run() note atop this file).
type settingsCmd struct {
	Show settingsShowCmd `cmd:"" default:"withargs" hidden:"" help:"Show core settings (default)"`
	Set  settingsSetCmd  `cmd:"" help:"Set a core setting"`
}

type settingsShowCmd struct{}

type settingsSetCmd struct {
	Key   string `arg:"" enum:"model,thinking" help:"Setting to change: model or thinking"`
	Value string `arg:"" help:"New value"`
}

// ── mcp ──────────────────────────────────────────────────────────────────

type mcpCmd struct {
	List    mcpListCmd    `cmd:"" default:"1" help:"List MCP servers"`
	Add     mcpAddCmd     `cmd:"" help:"Add an MCP server (transport inferred: --command → local, --url → remote)"`
	Rm      mcpRmCmd      `cmd:"" aliases:"remove" help:"Remove an MCP server"`
	Enable  mcpEnableCmd  `cmd:"" help:"Enable a server"`
	Disable mcpDisableCmd `cmd:"" help:"Disable a server (keeps its config)"`
}

type mcpListCmd struct{}

type mcpAddCmd struct {
	Name    string `arg:"" help:"Server name"`
	Command string `help:"Local server: command + args, e.g. \"npx -y @mcp/fs\""`
	URL     string `help:"Remote server: server URL"`
	Bearer  string `help:"Remote: sugar for --header \"Authorization: Bearer <token>\""`
	// []string (not map[string]string) gives a repeatable flag:
	// --env KEY=VAL --env KEY2=VAL2, parsed as "KEY=VAL"/"KEY:VAL" pairs in
	// Run() (see parseKV in kong_run.go). map[string]string would instead
	// require a single --env=KEY=VAL;KEY2=VAL2 with mapsep, a clunkier UX.
	Env      []string `help:"Local: env var KEY=VAL (repeatable)"`
	Header   []string `help:"Remote: HTTP header KEY:VAL (repeatable)"`
	Disabled bool     `help:"Add the server disabled (default: enabled)"`
}

type mcpRmCmd struct {
	Name string `arg:"" help:"Server name"`
}

type mcpEnableCmd struct {
	Name string `arg:"" help:"Server name"`
}

type mcpDisableCmd struct {
	Name string `arg:"" help:"Server name"`
}

// ── memo ─────────────────────────────────────────────────────────────────

type memoCmd struct {
	Query   string `arg:"" optional:"" help:"Full-text search query (omit to list)"`
	All     bool   `help:"Include memories from ALL projects (not just this one)"`
	Global  bool   `help:"Only global (cross-project) memories"`
	Content bool   `help:"Show each memory's content preview"`
	Limit   int    `default:"10" help:"Max results per page"`
	Skip    int    `default:"0" help:"Pagination offset"`
}

// ── schedules ────────────────────────────────────────────────────────────

type schedulesCmd struct {
	JSON bool `name:"json" help:"Output as JSON"`
}
