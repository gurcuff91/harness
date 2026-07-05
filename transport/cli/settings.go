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
			typ, _ := srv["type"].(string)
			enabled, _ := srv["enabled"].(bool)
			detail := ""
			switch typ {
			case "local":
				if cmd, ok := srv["command"].([]any); ok {
					parts := make([]string, len(cmd))
					for i, p := range cmd {
						parts[i], _ = p.(string)
					}
					detail = strings.Join(parts, " ")
				}
			case "remote":
				detail, _ = srv["url"].(string)
			}
			// State column reflects real connection when enabled.
			state := "disabled"
			if enabled {
				if st, ok := statusByName[n]; ok {
					if st.Connected {
						state = fmt.Sprintf("\u2713 connected (%d tools)", st.ToolCount)
					} else if st.Error != "" {
						state = "\u2717 failed: " + st.Error
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

// MCPAddOpts carries the parsed flags for `harness mcp add`.
type MCPAddOpts struct {
	Local    bool
	Remote   bool
	Command  string            // local: full command string ("npx -y @mcp/fs")
	URL      string            // remote
	Env      map[string]string // local
	Headers  map[string]string // remote
	Disabled bool              // default enabled; --disabled turns it off
}

// RunMCPAdd creates (or replaces) an MCP server. The name is positional; the
// shape is driven by --local/--remote. Content validation (type/command/url)
// happens server-side (422 surfaced verbatim).
func RunMCPAdd(ctx context.Context, a *agent.Agent, name string, opts MCPAddOpts, output string) error {
	if name == "" {
		return fmt.Errorf("server name required: harness mcp add <name> [--local|--remote] ...")
	}
	if opts.Local == opts.Remote { // both false or both true
		return fmt.Errorf("specify exactly one of --local or --remote")
	}

	srv := map[string]any{"enabled": !opts.Disabled}
	if opts.Local {
		srv["type"] = "local"
		if opts.Command != "" {
			srv["command"] = strings.Fields(opts.Command)
		}
		if len(opts.Env) > 0 {
			srv["env"] = opts.Env
		}
	} else {
		srv["type"] = "remote"
		if opts.URL != "" {
			srv["url"] = opts.URL
		}
		if len(opts.Headers) > 0 {
			srv["headers"] = opts.Headers
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
