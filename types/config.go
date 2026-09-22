package types

// MCPServer is the configuration of one MCP (Model Context Protocol) server.
// The transport is INFERRED, not declared: a server with a Command is local
// (spawns a process); a server with a URL is remote (dials HTTP). Declaring
// both is invalid. Servers are enabled by default — set Disabled to turn one
// off without deleting it.
//
// On-disk shape (settings.json):
//
//	local:  { "command": "npx", "args": ["-y", "@mcp/fs"], "env": {...} }
//	remote: { "url": "https://…/mcp", "headers": {...} }
//	off:    add "disabled": true to either
//
// Lives in types (not internal/config) because it's part of the public HTTP
// API contract — the client SDK needs it without pulling in the config package.
type MCPServer struct {
	Command  string            `json:"command,omitempty"`  // local: executable
	Args     []string          `json:"args,omitempty"`     // local: arguments
	URL      string            `json:"url,omitempty"`      // remote: server URL
	Env      map[string]string `json:"env,omitempty"`      // local: process env vars
	Headers  map[string]string `json:"headers,omitempty"`  // remote: custom HTTP headers
	Cwd      string            `json:"cwd,omitempty"`      // local: working directory (optional)
	Timeout  int               `json:"timeout,omitempty"`  // ms for connect (initialize+tools/list); 0 = default 5000
	Disabled bool              `json:"disabled,omitempty"` // enabled by default; set true to skip
}

// IsRemote reports whether the server is a remote (HTTP) transport. A server is
// remote when it has a URL; otherwise it is local (stdio). Validation
// guarantees exactly one of Command/URL is set before this is consulted.
func (s MCPServer) IsRemote() bool { return s.URL != "" }

// Argv returns the full local command line (executable + args) for the stdio
// transport. Empty when Command is unset.
func (s MCPServer) Argv() []string {
	if s.Command == "" {
		return nil
	}
	return append([]string{s.Command}, s.Args...)
}

// CustomProvider is the configuration of one user-defined LLM provider —
// e.g. a proxy or self-hosted endpoint speaking an existing API dialect.
// Only Type "openai" is supported for now (an OpenAI Chat Completions-
// compatible HTTP endpoint). The map key it's stored under (settings.json's
// "provider" collection) IS the provider name used everywhere else:
// `<name>/<model>` in provider/model strings and GET /api/providers — there
// is no separate name field here, same as MCPServer.
//
// Authentication is ALWAYS carried in Headers, never a separate API key —
// there's no reliable way to guess, for an arbitrary custom backend,
// whether it expects "Authorization: Bearer <token>", a custom header
// ("X-Api-Key: ..."), or several headers combined. `harness connect
// <name> <key>` does NOT apply to a custom provider (it's rejected, same
// as it already is for auto-detected Ollama) — put whatever credential the
// endpoint needs directly in Headers.
//
// On-disk shape (settings.json):
//
//	{ "provider": { "my-proxy": { "type": "openai", "url": "https://…/v1", "headers": {"X-Api-Key": "..."} } } }
//
// Lives in types (not internal/config) for the same reason MCPServer does:
// part of the public HTTP API contract, needed by the client SDK without
// pulling in the config package.
type CustomProvider struct {
	Type      string            `json:"type"`                 // "openai" — only supported value for now
	URL       string            `json:"url"`                  // base URL for chat completions
	ModelsURL string            `json:"models_url,omitempty"` // optional; falls back to <url>/models
	Headers   map[string]string `json:"headers,omitempty"`    // extra HTTP headers, sent on every request — this is where ANY authentication goes
	Display   string            `json:"display,omitempty"`    // optional human-friendly name; falls back to the map key
	Disabled  bool              `json:"disabled,omitempty"`   // enabled by default; set true to skip

	// ReasoningSplit, when true, adds "reasoning_split": true to every
	// chat-completions request — the same wire flag the built-in
	// `minimax` provider always sends. Opt-in and false by default: most
	// OpenAI-compatible backends don't recognize this field at all (an
	// unrecognized field is normally ignored, but there's no universal
	// guarantee of that), and plenty that DO recognize it don't need it
	// (they either have no thinking mode, or already separate it some
	// other way). Backends that mirror MiniMax's own behavior — emitting
	// reasoning inline inside the `content` field as literal
	// "<think>...</think>" wrapping the final answer when this flag is
	// left off — need it set to get their thinking routed to the
	// dedicated `reasoning_content` field instead, which harness already
	// parses into EventStreamThinkingDelta. Confirmed live against a
	// Kaiban gateway proxying to MiniMax: identical behavior to the
	// direct MiniMax API. Set this to true for any custom provider
	// fronting a MiniMax-compatible backend; leave it off (the default)
	// for everything else.
	ReasoningSplit bool `json:"reasoning_split,omitempty"`
}
