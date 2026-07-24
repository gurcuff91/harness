package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gurcuff91/harness/agent"
)

// RunSettings prints the current core settings (active_model, thinking_level).
func RunSettings(ctx context.Context, a *agent.Agent, output string) error {
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	data, err := c.GetSettings()
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	var s map[string]any
	json.Unmarshal(data, &s)

	switch output {
	case "json":
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(b))
	default:
		model, _ := s["active_model"].(string)
		thinking, _ := s["thinking_level"].(string)
		if model == "" {
			model = "(none)"
		}
		if thinking == "" {
			thinking = "off"
		}
		fmt.Printf("%-16s %s\n", "model", model)
		fmt.Printf("%-16s %s\n", "thinking", thinking)
	}
	return nil
}

// RunSettingsSet updates a core setting. Accepted keys (short form): "model",
// "thinking". Maps to a PATCH on /api/settings. Validation (e.g. thinking
// levels) lives in the API/SettingsManager; we surface its error verbatim.
func RunSettingsSet(ctx context.Context, a *agent.Agent, key, value, output string) error {
	var field string
	switch key {
	case "model":
		field = "active_model"
	case "thinking":
		field = "thinking_level"
	default:
		return fmt.Errorf("unknown setting %q (want: model | thinking)", key)
	}
	if value == "" {
		return fmt.Errorf("a value is required: harness settings set %s <value>", key)
	}

	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	if _, err := c.PatchSettings(map[string]any{field: value}); err != nil {
		return fmt.Errorf("set %s: %w", key, err)
	}
	if output == "json" {
		fmt.Printf("{\"%s\":\"%s\"}\n", field, value)
	} else {
		fmt.Printf("%s = %s\n", key, value)
	}
	return nil
}

// RunMCPList prints the configured MCP servers.
func RunMCPList(ctx context.Context, a *agent.Agent, output string) error {
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	data, err := c.GetMCPServers()
	if err != nil {
		return fmt.Errorf("list mcp: %w", err)
	}
	var servers map[string]map[string]any
	json.Unmarshal(data, &servers)

	// Cross-reference live connection status (connected? tool count? error?).
	type mcpStatus struct {
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
		ToolCount int    `json:"tool_count"`
		Error     string `json:"error"`
	}
	statusByName := map[string]mcpStatus{}
	if sd, err := c.GetMCPStatus(); err == nil {
		var sts []mcpStatus
		json.Unmarshal(sd, &sts)
		for _, st := range sts {
			statusByName[st.Name] = st
		}
	}

	switch output {
	case "json":
		b, _ := json.MarshalIndent(servers, "", "  ")
		fmt.Println(string(b))
	default:
		if len(servers) == 0 {
			fmt.Println("No MCP servers configured.")
			return nil
		}
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			srv := servers[n]
			url, _ := srv["url"].(string)
			disabled, _ := srv["disabled"].(bool)
			// Transport is inferred: a url is remote, otherwise local.
			typ := "local"
			detail := ""
			if url != "" {
				typ = "remote"
				detail = url
			} else {
				cmd, _ := srv["command"].(string)
				parts := []string{}
				if cmd != "" {
					parts = append(parts, cmd)
				}
				if args, ok := srv["args"].([]any); ok {
					for _, p := range args {
						if s, ok := p.(string); ok {
							parts = append(parts, s)
						}
					}
				}
				detail = strings.Join(parts, " ")
			}
			// State column reflects real connection when enabled.
			state := "disabled"
			if !disabled {
				if st, ok := statusByName[n]; ok {
					if st.Connected {
						state = fmt.Sprintf("\u2713 connected (%d tools)", st.ToolCount)
					} else if st.Error != "" {
						state = "\u2717 failed: " + truncateError(st.Error)
					} else {
						state = "\u2717 not connected"
					}
				} else {
					state = "enabled"
				}
			}
			fmt.Printf("%-16s %-8s %s  %s\n", n, typ, detail, state)
		}
	}
	return nil
}

// truncateError shortens a connection error to one readable line (server error
// bodies can be full HTML pages).
func truncateError(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 100
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// MCPAddOpts carries the parsed flags for `harness mcp add`. The transport is
// inferred: --command makes it local, --url makes it remote.
type MCPAddOpts struct {
	Command  string            // local: full command string ("npx -y @mcp/fs")
	URL      string            // remote: server URL
	Env      map[string]string // local
	Headers  map[string]string // remote
	Bearer   string            // remote: sugar — expands to Authorization: Bearer <token>
	Disabled bool              // default enabled; --disabled turns it off
}

// RunMCPAdd creates (or replaces) an MCP server. The name is positional; the
// transport is inferred from which of --command / --url is given (exactly one).
// Content validation happens server-side (422 surfaced verbatim).
func RunMCPAdd(ctx context.Context, a *agent.Agent, name string, opts MCPAddOpts, output string) error {
	if name == "" {
		return fmt.Errorf("server name required: harness mcp add <name> (--command … | --url …)")
	}
	hasCmd := strings.TrimSpace(opts.Command) != ""
	hasURL := strings.TrimSpace(opts.URL) != ""
	if hasCmd == hasURL { // both or neither
		return fmt.Errorf("specify exactly one of --command (local) or --url (remote)")
	}

	srv := map[string]any{}
	if opts.Disabled {
		srv["disabled"] = true
	}
	if hasCmd {
		// Split the command string into executable + args (canonical shape).
		fields := strings.Fields(opts.Command)
		srv["command"] = fields[0]
		if len(fields) > 1 {
			srv["args"] = fields[1:]
		}
		if len(opts.Env) > 0 {
			srv["env"] = opts.Env
		}
	} else {
		srv["url"] = opts.URL
		headers := opts.Headers
		// --bearer sugar: set Authorization unless the user already provided one.
		if opts.Bearer != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			if _, exists := headers["Authorization"]; !exists {
				headers["Authorization"] = "Bearer " + opts.Bearer
			}
		}
		if len(headers) > 0 {
			srv["headers"] = headers
		}
	}

	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	data, err := c.PutMCPServer(name, srv)
	if err != nil {
		return fmt.Errorf("add mcp %q: %w", name, err)
	}
	if output == "json" {
		fmt.Println(string(data))
	} else {
		fmt.Printf("MCP server added: %s\n", name)
	}
	return nil
}

// RunMCPSetEnabled toggles a server's disabled flag by name. It reads the
// current config (so it preserves command/url/env/headers), flips disabled,
// and writes it back. A missing server is a clean error.
func RunMCPSetEnabled(ctx context.Context, a *agent.Agent, name string, enabled bool, output string) error {
	if name == "" {
		verb := "enable"
		if !enabled {
			verb = "disable"
		}
		return fmt.Errorf("server name required: harness mcp %s <name>", verb)
	}

	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	// Load the whole collection and pick out the target so the round-trip
	// preserves every other field.
	data, err := c.GetMCPServers()
	if err != nil {
		return fmt.Errorf("list mcp: %w", err)
	}
	var servers map[string]map[string]any
	json.Unmarshal(data, &servers)
	srv, ok := servers[name]
	if !ok {
		return fmt.Errorf("mcp server %q not found", name)
	}

	if enabled {
		delete(srv, "disabled") // enabled is the default → omit the field
	} else {
		srv["disabled"] = true
	}

	if _, err := c.PutMCPServer(name, srv); err != nil {
		return fmt.Errorf("update mcp %q: %w", name, err)
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	if output == "json" {
		fmt.Printf("{\"name\":%q,\"disabled\":%t}\n", name, !enabled)
	} else {
		fmt.Printf("MCP server %s: %s\n", state, name)
	}
	return nil
}

// RunMCPRemove deletes an MCP server (404 surfaced as a clean error).
func RunMCPRemove(ctx context.Context, a *agent.Agent, name, output string) error {
	if name == "" {
		return fmt.Errorf("server name required: harness mcp rm <name>")
	}
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	if _, err := c.DeleteMCPServer(name); err != nil {
		return fmt.Errorf("remove mcp %q: %w", name, err)
	}
	fmt.Printf("MCP server removed: %s\n", name)
	return nil
}
