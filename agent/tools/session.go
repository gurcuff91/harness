// Package tools — the SessionInfo built-in tool: a small, purely
// informational snapshot of the session's own identity/config
// (id, cwd, name, model, thinking, created_at), environment (harness
// version, connected MCPs, owned schedules), and accumulated usage
// (tokens/cache/cost/context). Always registered like any other built-in
// (see agent.go's buildSessionTools) — no dedicated AgentOptions.EnableX
// flag; DisallowedTools is the only way to turn it off.
//
// This package used to also hold SessionSearch (full-text search over the
// session's own history) — removed for being largely unused in practice
// and redundant with persistent memory (MemoWrite/MemoSearch); see
// docs/plans/2026-09-22-sessionsearch-removal.md.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// SessionInfoSnapshot is the JSON shape SessionInfo returns. Deliberately
// excludes CompactCount and LastActiveAt (neither carries actionable value
// for the model — CompactCount is an internal bookkeeping counter, and
// LastActiveAt is redundant for a session that is, by definition, active
// right now) and MaxIterations (implementation detail of the ReAct loop's
// iteration budget, not something the model should reason about). Mirrors
// the same fields the TUI's `/info` panel shows, minus those two.
type SessionInfoSnapshot struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd"`
	Name      string `json:"name"`
	Model     string `json:"model"`
	Thinking  string `json:"thinking"`
	CreatedAt string `json:"created_at"`

	// Environment — harness version and counts of other active integrations.
	Version       string `json:"version"`
	MCPConnected  int    `json:"mcp_connected"`
	ScheduleCount int    `json:"schedule_count"`

	// Usage — accumulated over the session's lifetime. Exposing this to the
	// model is a deliberate reversal of an earlier, stricter convention
	// ("never surface spend to the model") — Gus's explicit call: this is
	// valuable self-awareness for the model (e.g. "am I burning a lot of
	// context/budget on this turn"), same data the TUI footer already shows
	// the human.
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	CacheRead     int     `json:"cache_read"`
	CacheWrite    int     `json:"cache_write"`
	CostUSD       float64 `json:"cost_usd"`
	ContextUsage  float64 `json:"context_usage"`
	ContextWindow int     `json:"context_window"`
}

// SessionInfoProvider supplies the live snapshot SessionInfo returns — the
// agent injects a closure reading its own Session, keeping this package
// free of any dependency on agent/store's concrete types.
type SessionInfoProvider func() SessionInfoSnapshot

// SessionInfo returns the SessionInfo tool: a no-argument snapshot of the
// session's own identity/config.
func SessionInfo(provider SessionInfoProvider) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSessionInfo,
			Description: "Return a snapshot of THIS session's own identity, configuration, environment, and accumulated usage: id, working directory, name, active model, thinking level, harness version, connected MCP servers, owned schedules, and token/cache/cost/context-window usage so far.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			snap := provider()
			out, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal session info: %w", err)
			}
			return string(out), nil
		},
	}
}
