package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/client"
)

// RunSettings prints the current core settings (active_model, thinking_level).
func RunSettings(ctx context.Context, a *agent.Agent, output string) error {
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	s, err := c.GetSettings()
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}

	switch output {
	case "json":
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(b))
	default:
		model := s.ActiveModel
		thinking := s.ThinkingLevel
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

	servers, err := c.GetMCPServers()
	if err != nil {
		return fmt.Errorf("list mcp: %w", err)
	}

	// Cross-reference live connection status (connected? tool count? error?).
	statusByName := map[string]client.MCPStatus{}
	if sts, err := c.GetMCPStatus(); err == nil {
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
			// Transport is inferred: a url is remote, otherwise local.
			typ := "local"
			detail := strings.Join(srv.Argv(), " ")
			if srv.IsRemote() {
				typ = "remote"
				detail = srv.URL
			}
			// State column reflects real connection when enabled.
			state := "disabled"
			if !srv.Disabled {
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

	var srv client.MCPServer
	srv.Disabled = opts.Disabled
	if hasCmd {
		// Split the command string into executable + args (canonical shape).
		fields := strings.Fields(opts.Command)
		srv.Command = fields[0]
		if len(fields) > 1 {
			srv.Args = fields[1:]
		}
		srv.Env = opts.Env
	} else {
		srv.URL = opts.URL
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
		srv.Headers = headers
	}

	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	saved, err := c.PutMCPServer(name, srv)
	if err != nil {
		return fmt.Errorf("add mcp %q: %w", name, err)
	}
	if output == "json" {
		b, _ := json.Marshal(saved)
		fmt.Println(string(b))
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
	servers, err := c.GetMCPServers()
	if err != nil {
		return fmt.Errorf("list mcp: %w", err)
	}
	srv, ok := servers[name]
	if !ok {
		return fmt.Errorf("mcp server %q not found", name)
	}

	// enabled is the default (Disabled omitempty) → toggle the single field; the
	// rest of the struct round-trips untouched.
	srv.Disabled = !enabled

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
