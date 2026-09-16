// Package tools — SessionInfo + SessionSearch built-in tools.
//
// Two views of the same concept ("information about this session"), gated
// behind the single AgentOptions.EnableSessionInfo flag:
//   - SessionInfo returns a small snapshot of the session's own identity/
//     config (id, cwd, name, model, thinking, created_at).
//   - SessionSearch full-text searches the ENTIRE conversation history
//     (including anything already folded into a compaction checkpoint) via
//     the session's own SessionStore.SearchMessages — this tool is a thin
//     adapter over that, with NO knowledge of which backend implements it
//     (FTS5-over-SQLite for FileStore, ErrSearchNotSupported for
//     InMemoryStore, or whatever a future custom SessionStore does). See
//     docs/plans/2026-09-16-sessionsearch-into-store-design.md.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/agent/store"
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

// ── SessionSearch ──────────────────────────────────────────────────────────

// sessionSearchInput is the JSON input schema for the SessionSearch tool.
type sessionSearchInput struct {
	Query string `json:"query" validate:"required"`
	Limit int    `json:"limit,omitempty"`
}

const (
	sessionSearchDefaultLimit = 10
	sessionSearchMaxLimit     = 25
)

// SessionSearchFunc performs the actual search — the agent injects a
// closure over its own Session.SearchMessages(query, limit), keeping this
// tool free of any knowledge of WHICH SessionStore backend is behind it
// (FileStore, InMemoryStore, or a future custom SDK port). A backend that
// doesn't support search returns store.ErrSearchNotSupported, which the
// tool surfaces as a plain, honest error — not a silent empty result.
type SessionSearchFunc func(query string, limit int) ([]store.SearchResult, error)

// SessionSearch returns the SessionSearch tool: full-text search over the
// session's entire conversation history (plain text only — no tool_call,
// no tool_result, no thinking; the filtering rules live in whichever
// SessionStore backend implements SearchMessages).
func SessionSearch(search SessionSearchFunc) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSessionSearch,
			Description: "Full-text search over the complete conversation history of this session — every user and assistant message ever exchanged here. Returns matching snippets, most relevant first. Use this to recover something discussed earlier in this conversation that is no longer visible in the current context.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Search terms."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 25, "default": 10, "description": "Maximum number of results to return."}
				},
				"required": ["query"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args sessionSearchInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if err := requireFields(&args); err != nil {
				return err.Error(), err
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("query is required")
			}
			limit := args.Limit
			switch {
			case limit <= 0:
				limit = sessionSearchDefaultLimit
			case limit > sessionSearchMaxLimit:
				limit = sessionSearchMaxLimit
			}

			results, err := search(args.Query, limit)
			if errors.Is(err, store.ErrSearchNotSupported) {
				return "", fmt.Errorf("this session's storage backend does not support search")
			}
			if err != nil {
				return "", fmt.Errorf("session search: %w", err)
			}
			if results == nil {
				results = []store.SearchResult{}
			}
			out, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal session search results: %w", err)
			}
			return string(out), nil
		},
	}
}
