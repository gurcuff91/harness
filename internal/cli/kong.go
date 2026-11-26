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
// telegram`) AND has real subcommands (`telegram list`) can't put its own action in that parent struct's Run(): it
// would fire a second time after every subcommand, and the subcommand's
// --help would inherit all the parent's flags too. The fix used throughout
// below is the same one TUI uses at the root: the action lives in its own
// hidden default:"withargs" child (e.g. telegramRunCmd), a
// sibling of the real subcommands — never a Run() directly on a struct that
// also declares cmd:"" children.
package cli

import (
	"strings"

	"github.com/alecthomas/kong"
)

// ── Root grammar ─────────────────────────────────────────────────────────

// CLI is the root Kong grammar for harness. TUI is the default command
// (hidden from the top-level command list, but selected when no other
// command is named).
var CLI struct {
	Version kong.VersionFlag `short:"v" help:"Print the version and exit"`

	TUI tuiCmd `cmd:"" default:"withargs" hidden:"" help:"Interactive TUI (default)"`

	Serve serveCmd `cmd:"" help:"Start the HTTP/SSE server (headless)"`

	GenSecret genSecretCmd `name:"gen-secret" cmd:"" help:"Print a new secret for harness serve --secret (or HARNESS_SECRET)"`

	Telegram telegramCmd `cmd:"" help:"Telegram bot token (a session listens to the bot with --telegram-allow)"`

	Slack slackCmd `cmd:"" help:"Slack account for input channels (harness slack login)"`

	ACP acpCmd `cmd:"" help:"Run as an Agent Client Protocol agent over stdio (for Zed and other ACP clients; it takes no flags: set HARNESS_WITH_COLLEAGUES=1 / HARNESS_WITH_SCHEDULER=1 to turn the extras on)"`

	// ── Management ───────────────────────────────────────────────────────

	Providers providersCmd `cmd:"" help:"List providers"`

	Connect connectCmd `cmd:"" help:"Connect a provider"`

	Disconnect disconnectCmd `cmd:"" help:"Disconnect a provider"`

	Sessions sessionsCmd `cmd:"" help:"List sessions (current directory, or --all)"`

	Delete deleteCmd `cmd:"" help:"Delete a session"`

	// ── Settings ─────────────────────────────────────────────────────────

	Settings settingsCmd `cmd:"" help:"Show or set core settings"`

	MCP mcpCmd `cmd:"" help:"Manage MCP servers"`

	Provider providerCmd `cmd:"" help:"Manage custom OpenAI-compatible providers"`

	Memo memoCmd `cmd:"" help:"Search/list memories (read-only — the agent writes memories via its tools)"`

	Schedules schedulesCmd `cmd:"" help:"List cron-scheduled prompts (read-only — the agent creates them via its tools)"`
}

// ── TUI / one-shot prompt ────────────────────────────────────────────────

type tuiCmd struct {
	Model    string `help:"Model (provider/model)"`
	Thinking string `enum:",off,low,medium,high,xhigh,max" default:"" help:"Thinking level"`
	Resume   string `help:"Resume session by id"`
	// SlackChannels binds the startup session to Slack conversations (id or
	// name, repeatable): mentions there become its prompts, its replies are
	// posted back. Uses the `harness slack login` account.
	SlackChannels []string `name:"slack-channel" sep:"none" help:"Listen to a Slack channel or DM (id or name; repeatable): mentions there become prompts of this session and its replies are posted back. Needs harness slack login"`
	// TelegramAllow binds the startup session to the Telegram bot: messages
	// of these users (repeatable, or comma-separated) become its prompts.
	TelegramAllow     []string `name:"telegram-allow" help:"Listen to the Telegram bot: messages from these trusted @usernames (repeatable; commas also work; or TELEGRAM_ALLOW) become prompts of this session, and its replies go back to their chat. Needs a bot token (--telegram-token, TELEGRAM_BOT_TOKEN or harness telegram token)"`
	telegramTokenFlag `embed:""`
	Prompt            string `short:"p" help:"Run a single-turn prompt instead of the interactive TUI"`
	Output            string `enum:"text,json,json-stream" default:"text" help:"Output mode (with --prompt/-p)"`

	// Initial is an optional trailing positional: harness "fix the login bug" opens the interactive
	// TUI with that message already submitted (what Herdr's tab.new --task and `hc run harness
	// "task"` send). Several words are joined with a space. It is a leaf of the root, so it does
	// not clash with the named subcommands (Kong forbids a positional only next to cmd:"" children
	// IN THE SAME struct, and tuiCmd has none); a subcommand name WINS over a prompt, and a prompt
	// that is exactly one subcommand name goes after `--` (see parseArgs in app.go: Kong alone does
	// NOT make `--` escape a subcommand name at the root, because the positional belongs to this
	// leaf and not to the root node, so `harness -- serve` would still run serve).
	withFlags `embed:""`

	Initial []string `arg:"" optional:"" name:"prompt" help:"First message: opens the interactive TUI with it already submitted (words are joined; a prompt that is exactly a command name goes after --; use -p for a one-shot run with no TUI)"`
}

// initialPrompt is the positional prompt as one string: the words joined with a single space and
// the ends trimmed, exactly as the editor trims what the user types. Blank is "no prompt".
func (c *tuiCmd) initialPrompt() string {
	return strings.TrimSpace(strings.Join(c.Initial, " "))
}

// ── serve ────────────────────────────────────────────────────────────────

type serveCmd struct {
	Addr              string `arg:"" help:"Address to listen on: :8080 or 127.0.0.1:8080 listen on this machine only (localhost); 0.0.0.0:8080 exposes it to the network (no authentication: anyone who reaches it can use your sessions, providers and tools)"`
	withFlags         `embed:""`
	telegramTokenFlag `embed:""`

	// Secret protects the server: every request but GET /api/server and the API docs must
	// carry it (X-Harness-Sec header or Authorization: Bearer). It is not a Kong env tag for the
	// same reason as the Telegram token: an empty HARNESS_SECRET must not fail the command
	// (serveSecret reads the environment itself).
	Secret   string `name:"secret" help:"Require this secret on every request (X-Harness-Sec header or Authorization: Bearer); make one with harness gen-secret. Prefer the HARNESS_SECRET environment variable: a flag is visible in the process list. Without TLS it travels in the clear"`
	Insecure bool   `name:"insecure" help:"Allow listening beyond localhost WITHOUT a secret (anyone who reaches the port can use your sessions, providers and tools)"`
}

// genSecretCmd prints a new server secret.
type genSecretCmd struct{}

// telegramTokenFlag is the Telegram bot token a command registers the channel
// with, shared by the TUI and serve so the name and help cannot drift. Not a
// Kong env tag: an empty TELEGRAM_BOT_TOKEN must not fail the command
// (channels/telegram.ResolveToken reads the environment itself).
type telegramTokenFlag struct {
	TelegramToken string `name:"telegram-token" help:"Telegram bot token for this process (instead of TELEGRAM_BOT_TOKEN or the saved one); a session listens to the bot with --telegram-allow (TUI) or channels:[{type:telegram, ref:<users>}] (API)"`
}

// ── acp ──────────────────────────────────────────────────────────────────

// acpCmd has no flags: an ACP client (Zed) launches this exact command as a
// sub-process and speaks JSON-RPC to it over stdin/stdout — there is no
// terminal for flags to be typed into, and every per-session setting (model,
// thinking) is negotiated over the protocol itself instead (see
// acp's session config options). The two opt-in extras (colleagues, the scheduler) are
// the exception to "negotiated over the protocol": they decide which tools the AGENT has, which is
// fixed before any session exists, so they come from HARNESS_WITH_COLLEAGUES / HARNESS_WITH_SCHEDULER in
// the environment the editor starts the process with (see extras.go).
type acpCmd struct{}

// ── telegram ─────────────────────────────────────────────────────────────

// telegramCmd is the Telegram bot token group. The bot itself is no longer a
// command (v0.116.0): a session listens to it, as an input channel, with
// --telegram-allow (TUI) or channels on the API (serve).
type telegramCmd struct {
	Token telegramTokenCmd `cmd:"" help:"Save the bot token, check it (--status) or remove it (--delete)"`
}

type telegramTokenCmd struct {
	Token  string `arg:"" optional:"" help:"Bot token to verify and save"`
	Status bool   `help:"Check the saved token instead of saving one"`
	Delete bool   `help:"Remove the saved token"`
}

// ── slack ────────────────────────────────────────────────────────────────

// slackCmd is the Slack account group: today only `login`, the account the
// input channels (--slack-channel, API channels) listen with. The Slack bot
// (`harness slack` with no subcommand) and `slack admin` were removed in
// v0.108.0 along with frontends/slack.
type slackCmd struct {
	Login slackLoginCmd `cmd:"" help:"Authenticate interactively (saves the login to harness's config stores)"`
}

type slackLoginCmd struct {
	Status bool `help:"Verify saved credentials instead of logging in"`
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
	Env               []string `help:"Local: env var KEY=VAL (repeatable)"`
	Header            []string `help:"Remote: HTTP header KEY:VAL (repeatable)"`
	Timeout           int      `help:"Timeout of ONE tool call in ms (default 300000 = 5 min); raise it for tools that run longer"`
	ConnectionTimeout int      `help:"Connect timeout in ms (initialize + tools/list; default 30000 = 30 s)"`
	Disabled          bool     `help:"Add the server disabled (default: enabled)"`
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

// ── provider ─────────────────────────────────────────────────────────────
//
// Distinct from the existing, plural `harness providers` (providersCmd
// above) — that one is the read-only listing of EVERY registered provider,
// built-in and custom alike. This one (singular) manages the custom ones a
// user configures, mirroring mcpCmd's own shape. A custom provider
// authenticates entirely via --header (whatever the endpoint needs —
// "Authorization: Bearer <token>", "X-Api-Key: ...", or several combined) —
// `harness connect <name> <key>` does NOT apply here and is rejected, same
// as it already is for auto-detected Ollama.

type providerCmd struct {
	List providerListCmd `cmd:"" default:"1" help:"List custom providers"`
	Add  providerAddCmd  `cmd:"" help:"Add a custom OpenAI-compatible provider"`
	Rm   providerRmCmd   `cmd:"" aliases:"remove" help:"Remove a custom provider"`
}

type providerListCmd struct{}

type providerAddCmd struct {
	Name      string `arg:"" help:"Provider name (must not collide with a built-in provider)"`
	Type      string `default:"openai" enum:"openai,anthropic" help:"API dialect: openai (Chat Completions) or anthropic (Messages — also what to use to share another harness instance's claude-oauth via its proxy)"`
	URL       string `required:"" help:"Base URL: openai → chat completions base (incl. /v1); anthropic → API root WITHOUT /v1, e.g. https://api.anthropic.com or http://host:8080/api/providers/claude-oauth/proxy"`
	ModelsURL string `help:"Models listing URL (default: <url>/models, or <url>/v1/models for anthropic)"`
	// []string (not map[string]string) — same repeatable-flag UX as mcpAddCmd.Header.
	Header   []string `help:"HTTP header KEY:VAL (repeatable) — put any authentication here (e.g. Authorization:Bearer <token> or X-Api-Key:...)"`
	Display  string   `help:"Human-friendly display name (default: the provider name)"`
	Disabled bool     `help:"Add the provider disabled (default: enabled)"`
	// ReasoningSplit: opt-in wire flag some MiniMax-compatible backends
	// need to route thinking into reasoning_content instead of leaking it
	// inline inside content as literal "<think>...</think>" — see
	// types.CustomProvider.ReasoningSplit's doc comment.
	ReasoningSplit bool `help:"Send \"reasoning_split\": true on every request — needed for MiniMax-compatible backends to separate thinking from the final answer (default: off)"`
}

type providerRmCmd struct {
	Name string `arg:"" help:"Provider name"`
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
