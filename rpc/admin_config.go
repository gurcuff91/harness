package rpc

import (
	"context"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/types"
)

// ── settings ─────────────────────────────────────────────────────────────

func adminSettingsOps() []Op {
	return []Op{
		Define("admin/settings/get", "Returns the global defaults (active model and thinking level).", opAdmSettingsGet),
		Define("admin/settings/set_active_model", "Stores the default \"provider/model\" and returns the settings. Does not touch open sessions.", opAdmSettingsSetModel),
		Define("admin/settings/set_thinking_level", "Stores the default thinking level (off|low|medium|high|xhigh|max) and returns the settings. Does not touch open sessions.", opAdmSettingsSetThinking),
	}
}

func opAdmSettingsGet(_ context.Context, s *Server, _ Empty) (admin.SettingsValues, error) {
	return s.Admin().Settings.Get(), nil
}

// AdminSettingsSetActiveModelParams are the params of
// admin/settings/set_active_model.
type AdminSettingsSetActiveModelParams struct {
	Model string `json:"model"`
}

func opAdmSettingsSetModel(_ context.Context, s *Server, p AdminSettingsSetActiveModelParams) (admin.SettingsValues, error) {
	if err := s.Admin().Settings.SetActiveModel(p.Model); err != nil {
		return admin.SettingsValues{}, err
	}
	return s.Admin().Settings.Get(), nil
}

// AdminSettingsSetThinkingLevelParams are the params of
// admin/settings/set_thinking_level.
type AdminSettingsSetThinkingLevelParams struct {
	Level string `json:"level"`
}

func opAdmSettingsSetThinking(_ context.Context, s *Server, p AdminSettingsSetThinkingLevelParams) (admin.SettingsValues, error) {
	if err := s.Admin().Settings.SetThinkingLevel(p.Level); err != nil {
		return admin.SettingsValues{}, err
	}
	return s.Admin().Settings.Get(), nil
}

// ── mcp ──────────────────────────────────────────────────────────────────

func adminMCPOps() []Op {
	return []Op{
		Define("admin/mcp/list", "Lists the configured MCP servers.", opAdmMCPList),
		Define("admin/mcp/get", "Returns one configured MCP server.", opAdmMCPGet),
		Define("admin/mcp/put", "Validates and stores (or replaces) an MCP server.", opAdmMCPPut),
		Define("admin/mcp/delete", "Removes a configured MCP server.", opAdmMCPDelete),
		Define("admin/mcp/set_enabled", "Enables or disables a configured MCP server, keeping the rest of its config.", opAdmMCPSetEnabled),
	}
}

// AdminMCPNameParams carry an MCP server's name.
type AdminMCPNameParams struct {
	Name string `json:"name"`
}

// AdminMCPListResult is the result of admin/mcp/list.
type AdminMCPListResult struct {
	Servers map[string]types.MCPServer `json:"servers"`
}

func opAdmMCPList(_ context.Context, s *Server, _ Empty) (AdminMCPListResult, error) {
	m := s.Admin().MCP.List()
	if m == nil {
		m = map[string]types.MCPServer{}
	}
	return AdminMCPListResult{Servers: m}, nil
}

func opAdmMCPGet(_ context.Context, s *Server, p AdminMCPNameParams) (types.MCPServer, error) {
	return s.Admin().MCP.Get(p.Name)
}

// AdminMCPPutParams are the params of admin/mcp/put.
type AdminMCPPutParams struct {
	Name   string          `json:"name"`
	Server types.MCPServer `json:"server"`
}

func opAdmMCPPut(_ context.Context, s *Server, p AdminMCPPutParams) (Empty, error) {
	return Empty{}, s.Admin().MCP.Put(p.Name, p.Server)
}

func opAdmMCPDelete(_ context.Context, s *Server, p AdminMCPNameParams) (Empty, error) {
	return Empty{}, s.Admin().MCP.Delete(p.Name)
}

// AdminMCPSetEnabledParams are the params of admin/mcp/set_enabled.
type AdminMCPSetEnabledParams struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func opAdmMCPSetEnabled(_ context.Context, s *Server, p AdminMCPSetEnabledParams) (Empty, error) {
	return Empty{}, s.Admin().MCP.SetEnabled(p.Name, p.Enabled)
}

// ── memory ───────────────────────────────────────────────────────────────

func adminMemoryOps() []Op {
	return []Op{
		Define("admin/memory/configured", "Reports whether this agent has a memory store.", opAdmMemoryConfigured),
		Define("admin/memory/search", "Full-text searches memories (an empty query lists them), paginated. Empty when memory is not configured.", opAdmMemorySearch),
		Define("admin/memory/list", "Lists memories, paginated. Empty when memory is not configured.", opAdmMemoryList),
		Define("admin/memory/get", "Returns one memory. cwd is a project path, or \"<global>\" for a global memory.", opAdmMemoryGet),
		Define("admin/memory/put", "Creates or replaces one memory (an upsert by cwd+slug).", opAdmMemoryPut),
		Define("admin/memory/delete", "Removes one memory.", opAdmMemoryDelete),
	}
}

// AdminMemoryConfiguredResult is the result of admin/memory/configured.
type AdminMemoryConfiguredResult struct {
	Configured bool `json:"configured"`
}

func opAdmMemoryConfigured(_ context.Context, s *Server, _ Empty) (AdminMemoryConfiguredResult, error) {
	return AdminMemoryConfiguredResult{Configured: s.Admin().Memory.Configured()}, nil
}

// AdminMemorySearchParams are the params of admin/memory/search.
type AdminMemorySearchParams struct {
	// CWD filters: empty = every project, "<global>" = global only, a path =
	// that project plus the globals.
	CWD   string `json:"cwd,omitempty"`
	Query string `json:"query,omitempty"`
	// IncludeContent defaults to true.
	IncludeContent *bool `json:"include_content,omitempty"`
	Skip           int   `json:"skip,omitempty"`
	// Limit defaults to 10.
	Limit *int `json:"limit,omitempty"`
}

// AdminMemoryListParams are the params of admin/memory/list.
type AdminMemoryListParams struct {
	CWD            string `json:"cwd,omitempty"`
	IncludeContent *bool  `json:"include_content,omitempty"`
	Skip           int    `json:"skip,omitempty"`
	Limit          *int   `json:"limit,omitempty"`
}

// admPage applies the HTTP list handler's defaults and rejects negatives.
func admPage(include *bool, skip int, limit *int) (inc bool, lim int, err error) {
	inc, lim = true, 10
	if include != nil {
		inc = *include
	}
	if limit != nil {
		lim = *limit
	}
	if skip < 0 || lim < 0 {
		return false, 0, errInvalid("skip and limit must not be negative")
	}
	return inc, lim, nil
}

func opAdmMemorySearch(_ context.Context, s *Server, p AdminMemorySearchParams) (memory.SearchResult, error) {
	inc, lim, err := admPage(p.IncludeContent, p.Skip, p.Limit)
	if err != nil {
		return memory.SearchResult{}, err
	}
	return admMemResult(s.Admin().Memory.Search(p.CWD, p.Query, inc, p.Skip, lim))
}

func opAdmMemoryList(_ context.Context, s *Server, p AdminMemoryListParams) (memory.SearchResult, error) {
	inc, lim, err := admPage(p.IncludeContent, p.Skip, p.Limit)
	if err != nil {
		return memory.SearchResult{}, err
	}
	return admMemResult(s.Admin().Memory.List(p.CWD, inc, p.Skip, lim))
}

func admMemResult(r memory.SearchResult, err error) (memory.SearchResult, error) {
	if r.Results == nil {
		r.Results = []memory.Memory{}
	}
	return r, err
}

// AdminMemoryKeyParams identify one memory.
type AdminMemoryKeyParams struct {
	// CWD is required: a project path, or "<global>" for a global memory.
	CWD  string `json:"cwd"`
	Slug string `json:"slug"`
}

func opAdmMemoryGet(_ context.Context, s *Server, p AdminMemoryKeyParams) (memory.Memory, error) {
	return s.Admin().Memory.Get(p.CWD, p.Slug)
}

// AdminMemoryPutParams are the params of admin/memory/put.
type AdminMemoryPutParams struct {
	CWD     string `json:"cwd"`
	Slug    string `json:"slug"`
	Content string `json:"content"`
}

// AdminMemoryPutResult is the result of admin/memory/put.
type AdminMemoryPutResult struct {
	Memory memory.Memory `json:"memory"`
	// Created is true for a new memory, false when one was replaced.
	Created bool `json:"created"`
}

func opAdmMemoryPut(_ context.Context, s *Server, p AdminMemoryPutParams) (AdminMemoryPutResult, error) {
	m, created, err := s.Admin().Memory.Put(p.CWD, p.Slug, p.Content)
	if err != nil {
		return AdminMemoryPutResult{}, err
	}
	return AdminMemoryPutResult{Memory: m, Created: created}, nil
}

func opAdmMemoryDelete(_ context.Context, s *Server, p AdminMemoryKeyParams) (Empty, error) {
	return Empty{}, s.Admin().Memory.Delete(p.CWD, p.Slug)
}

// ── schedules ────────────────────────────────────────────────────────────

func adminSchedulesOps() []Op {
	return []Op{
		Define("admin/schedules/list", "Lists the cron-scheduled prompts, sorted by slug (read-only).", opAdmSchedulesList),
	}
}

// AdminSchedulesListParams are the params of admin/schedules/list.
type AdminSchedulesListParams struct {
	// Owner keeps only the schedules that fire into this session id.
	Owner string `json:"owner,omitempty"`
}

// AdminSchedule is a schedule on the wire (schedule.Schedule has no JSON tags,
// so it would serialize in Go's capitalized field names).
type AdminSchedule struct {
	Slug    string `json:"slug"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Owner   string `json:"owner"`
	Runs    int    `json:"runs"`
	LastRun int64  `json:"last_run,omitempty"`
	Origin  string `json:"origin,omitempty"`
}

// AdminSchedulesListResult is the result of admin/schedules/list.
type AdminSchedulesListResult struct {
	Schedules []AdminSchedule `json:"schedules"`
}

func opAdmSchedulesList(_ context.Context, s *Server, p AdminSchedulesListParams) (AdminSchedulesListResult, error) {
	list, err := s.Admin().Schedules.List(p.Owner)
	if err != nil {
		return AdminSchedulesListResult{}, err
	}
	out := make([]AdminSchedule, 0, len(list))
	for _, sc := range list {
		out = append(out, AdminSchedule{Slug: sc.Slug, Cron: sc.Cron, Prompt: sc.Prompt, Owner: sc.Owner, Runs: sc.Runs, LastRun: sc.LastRun, Origin: sc.Origin})
	}
	return AdminSchedulesListResult{Schedules: out}, nil
}
