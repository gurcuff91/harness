// Run() methods for the "leaf" commands — the ones with no nested
// subcommands and no interactive agent/frontend of their own. Each Run() is
// a thin adapter: parse Kong's already-validated fields into the Run*()
// business-logic functions (commands.go, settings.go, memory.go,
// schedule.go) — this file only wires Kong's parsed struct fields to their
// call sites.
package cli

import (
	"strings"

	"github.com/gurcuff91/harness/admin"
)

// ── providers / connect / disconnect ──────────────────────────────────────

func (c *providersCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunProviders(ctx, ad, "text")
}

func (c *connectCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunConnect(ctx, ad, c.Name, c.APIKey, "text")
}

func (c *disconnectCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunDisconnect(ctx, ad, c.Name, "text")
}

// ── sessions / delete ──────────────────────────────────────────────────────

func (c *sessionsCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunSessions(ctx, ad, c.All, "text")
}

func (c *deleteCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunDelete(ctx, ad, c.SessionID, "text")
}

// ── settings ───────────────────────────────────────────────────────────────

// settingsShowCmd.Run fires on a bare `harness settings` — it's the hidden
// default child of settingsCmd (see the parent-Run() note in kong.go), so it
// runs once and only for that path, never after `settings set`.
func (c *settingsShowCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunSettings(ctx, ad, "text")
}

func (c *settingsSetCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunSettingsSet(ctx, ad, c.Key, c.Value, "text")
}

// ── mcp ────────────────────────────────────────────────────────────────────

func (c *mcpListCmd) Run() error {
	a := newAgent() // the live connection status of each server is the agent's
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunMCPList(ctx, a, "text")
}

func (c *mcpAddCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()

	env, err := parseKV(c.Env, "=")
	if err != nil {
		return err
	}
	headers, err := parseKV(c.Header, ":")
	if err != nil {
		return err
	}
	opts := MCPAddOpts{
		Command:  c.Command,
		URL:      c.URL,
		Bearer:   c.Bearer,
		Env:      env,
		Headers:  headers,
		Disabled: c.Disabled,

		Timeout:           c.Timeout,
		ConnectionTimeout: c.ConnectionTimeout,
	}
	return RunMCPAdd(ctx, ad, c.Name, opts, "text")
}

func (c *mcpRmCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunMCPRemove(ctx, ad, c.Name, "text")
}

func (c *mcpEnableCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunMCPSetEnabled(ctx, ad, c.Name, true, "text")
}

func (c *mcpDisableCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunMCPSetEnabled(ctx, ad, c.Name, false, "text")
}

func (c *providerListCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunProviderList(ctx, ad, "text")
}

func (c *providerAddCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()

	headers, err := parseKV(c.Header, ":")
	if err != nil {
		return err
	}
	opts := ProviderAddOpts{
		Type:           c.Type,
		URL:            c.URL,
		ModelsURL:      c.ModelsURL,
		Headers:        headers,
		Display:        c.Display,
		Disabled:       c.Disabled,
		ReasoningSplit: c.ReasoningSplit,
	}
	return RunProviderAdd(ctx, ad, c.Name, opts, "text")
}

func (c *providerRmCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunProviderRemove(ctx, ad, c.Name, "text")
}

// parseKV parses a slice of "key<sep>value" strings into a map — the []string
// (not map[string]string) field type is what preserves today's repeatable
// --env A=B --env C=D UX; see the comment on MCP.Add.Env in kong.go.
func parseKV(pairs []string, sep string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		idx := strings.Index(p, sep)
		if idx < 0 {
			return nil, errf("expected key%svalue, got %q", sep, p)
		}
		m[strings.TrimSpace(p[:idx])] = strings.TrimSpace(p[idx+len(sep):])
	}
	return m, nil
}

// ── memo ───────────────────────────────────────────────────────────────────

func (c *memoCmd) Run() error {
	mem := defaultMemory()
	if mem != nil {
		defer mem.Close()
	}
	ad := admin.New(admin.WithMemory(mem))
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()

	opts := MemoOpts{
		Query:   c.Query,
		All:     c.All,
		Global:  c.Global,
		Content: c.Content,
		Limit:   c.Limit,
		Skip:    c.Skip,
	}
	return RunMemo(ctx, ad, opts, "text")
}

// ── schedules ──────────────────────────────────────────────────────────────

func (c *schedulesCmd) Run() error {
	ad := admin.New()
	defer ad.Close()
	ctx, cancel := signalContext()
	defer cancel()
	output := "text"
	if c.JSON {
		output = "json"
	}
	return RunSchedules(ctx, ad, output)
}
