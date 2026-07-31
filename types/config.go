package types

// ProviderConfig is the generic, per-provider configuration harness stores
// verbatim (see internal/config.SettingsManager). Fields are optional; a zero
// value means "not configured" and the owning provider should fall back to
// its own default. Grows on demand — only add a field when a provider
// actually consumes it.
//
// Lives in types (not internal/config) because it's part of the public HTTP
// API contract — the client SDK (client.ProviderConfig) needs it without
// pulling in the config package.
type ProviderConfig struct {
	URL string `json:"url,omitempty"`
}

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
// Lives in types (not internal/config) for the same reason as ProviderConfig.
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
