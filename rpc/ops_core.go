package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/profiles"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	colleaguech "github.com/gurcuff91/harness/channels/colleague"
	slackch "github.com/gurcuff91/harness/channels/slack"
	telegramch "github.com/gurcuff91/harness/channels/telegram"
	"github.com/gurcuff91/harness/internal/version"
	"github.com/gurcuff91/harness/mcp"
)

// coreOps are `initialize` and the agent/* operations.
func coreOps() []Op {
	return []Op{
		Define("initialize", "Starts the connection: checks the protocol version and builds the agent from the configuration. Must be the first request, once.", opInitialize),
		Define("agent/register_tool", "Registers a tool implemented by the client, for every session opened from now on.", opRegisterTool),
		Define("agent/register_profile", "Registers (or replaces) a profile; sessions bound to it pick it up at their next turn.", opRegisterProfile),
		Define("agent/unregister_profile", "Removes a profile; bound sessions keep the name and lose its directives and tools.", opUnregisterProfile),
		Define("agent/profiles", "Lists the registered profile names.", opProfiles),
		Define("agent/active_sessions", "Lists the ids of the sessions open in this process.", opActiveSessions),
		Define("agent/mcp_status", "Reports the connection state of each configured MCP server.", opMCPStatus),
		afterReply(Define("agent/shutdown", "Replies, then closes every session and the agent and exits.", opShutdown), (*Server).requestStop),
	}
}

func opInitialize(_ context.Context, s *Server, p InitializeParams) (InitializeResult, error) {
	// Received (the reading goroutine) already moved new → initializing when
	// this request arrived; a second `initialize` finds it ready or busy.
	s.mu.Lock()
	if s.state == stateReady || s.initBuilding {
		s.mu.Unlock()
		return InitializeResult{}, rpcErr("conflict", "already initialized")
	}
	s.initBuilding = true
	s.state = stateInitializing
	s.mu.Unlock()

	a, chans, err := s.buildAgent(p.Config)
	if err != nil {
		s.mu.Lock()
		s.state = stateNew // a failed initialize can be retried
		s.initBuilding = false
		s.mu.Unlock()
		return InitializeResult{}, err
	}
	s.mu.Lock()
	s.agent, s.admin, s.state = a, a.Admin(), stateReady
	s.initBuilding = false
	s.mu.Unlock()
	return InitializeResult{
		ProtocolVersion: ProtocolVersion,
		HarnessVersion:  version.Version,
		Capabilities:    Capabilities{Channels: chans},
	}, nil
}

// kindOf validates a KindConfig against the allowed kinds; def is the kind of
// an omitted field, and pathKinds the ones that take a path.
func kindOf(field string, k *KindConfig, def string, allowed, pathKinds []string) (string, string, error) {
	if k == nil {
		return def, "", nil
	}
	kind := k.Kind
	if kind == "" {
		kind = def
	}
	ok := false
	for _, a := range allowed {
		ok = ok || a == kind
	}
	if !ok {
		return "", "", invalidParams("config.%s.kind %q is not one of: %s", field, k.Kind, strings.Join(allowed, ", "))
	}
	if k.Path != "" {
		has := false
		for _, a := range pathKinds {
			has = has || a == kind
		}
		if !has {
			return "", "", invalidParams("config.%s.path is not accepted by kind %q", field, kind)
		}
	}
	return kind, k.Path, nil
}

// buildAgent turns the configuration into a running agent. The stores it
// opens belong to the agent (Close releases them); on failure nothing is left
// open.
func (s *Server) buildAgent(cfg Config) (*agent.Agent, []string, error) {
	// Validate everything before opening anything.
	storeKind, storePath, err := kindOf("store", cfg.Store, "file", []string{"file", "memory"}, []string{"file"})
	if err != nil {
		return nil, nil, err
	}
	memKind, memPath, err := kindOf("memory", cfg.Memory, "none", []string{"sqlite", "none"}, []string{"sqlite"})
	if err != nil {
		return nil, nil, err
	}
	resKind := "" // omitted: nothing is discovered
	if cfg.Resources != nil && cfg.Resources.Kind != "" {
		resKind = cfg.Resources.Kind
		if resKind != "file" {
			return nil, nil, invalidParams("config.resources.kind %q is not one of: file", cfg.Resources.Kind)
		}
	}
	if cfg.ThinkingLevel != "" && !validThinking(cfg.ThinkingLevel) {
		return nil, nil, invalidParams("config.thinking_level %q is not one of: off, low, medium, high, xhigh", cfg.ThinkingLevel)
	}
	for name, srv := range cfg.MCPServers {
		if err := srv.Validate(); err != nil {
			return nil, nil, errInvalid("config.mcp_servers.%s: %v", name, err)
		}
	}
	chans, names, err := buildChannels(cfg.Channels)
	if err != nil {
		return nil, nil, err
	}
	toolList, err := s.bridgeTools(cfg.Tools)
	if err != nil {
		return nil, nil, err
	}
	profs := make(map[string]profiles.Profile, len(cfg.Profiles))
	for name, pc := range cfg.Profiles {
		pr, err := s.profileOf(name, pc)
		if err != nil {
			return nil, nil, err
		}
		profs[name] = pr
	}

	opts := agent.AgentOptions{
		ThinkingLevel:   cfg.ThinkingLevel,
		SystemPrompt:    cfg.SystemPrompt,
		Directives:      cfg.Directives,
		MaxIterations:   cfg.MaxIterations,
		MaxTokens:       cfg.MaxTokens,
		Profiles:        profs,
		Tools:           toolList,
		DisallowedTools: cfg.DisallowedTools,
		MCPServers:      cfg.MCPServers,
		EnableScheduler: cfg.EnableScheduler,
		EnableWebSearch: cfg.EnableWebSearch,
		Terminal:        cfg.Terminal,
		Channels:        chans,
	}
	var closers []func()
	fail := func(err error) (*agent.Agent, []string, error) {
		for _, c := range closers {
			c()
		}
		return nil, nil, err
	}
	switch storeKind {
	case "memory":
		opts.Store = store.NewInMemoryStore()
	default:
		fs, err := store.NewFileStore(storePath)
		if err != nil {
			return fail(errInvalid("config.store: %v", err))
		}
		opts.Store = fs
		closers = append(closers, func() { fs.Close() })
	}
	if memKind == "sqlite" {
		m, err := memory.OpenSQLite(memPath)
		if err != nil {
			return fail(errInvalid("config.memory: %v", err))
		}
		opts.Memory = m
		closers = append(closers, func() { m.Close() })
	}
	// Omitted = discover nothing, exactly as agent.New does; only "file" reads the
	// project's AGENTS.md, SYSTEM.md and skills from each session's cwd.
	if resKind == "file" {
		opts.ResourceLoader = resources.File()
	}
	return agent.New(opts), names, nil
}

func validThinking(l string) bool {
	switch l {
	case "off", "low", "medium", "high", "xhigh":
		return true
	}
	return false
}

// buildChannels registers the channels asked for by name, with the credentials
// the CLI reads (a Slack login, a Telegram token): a name whose credentials
// are missing is an `invalid` error naming it.
func buildChannels(names []string) ([]agent.Channel, []string, error) {
	var out []agent.Channel
	var got []string
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		switch n {
		case "colleagues":
			out = append(out, colleaguech.New())
		case "slack":
			creds, err := slackch.LoadCredentials()
			if err != nil || creds == nil || creds.Workspace == "" || creds.XoxC == "" {
				return nil, nil, errInvalid("config.channels: slack is not available: no Slack login (run harness slack login)")
			}
			out = append(out, slackch.New(creds.Workspace, creds.XoxC, creds.XoxD))
		case "telegram":
			token := telegramch.ResolveToken("")
			if token == "" {
				return nil, nil, errInvalid("config.channels: telegram is not available: no bot token (set %s or run harness telegram token)", telegramch.TokenEnv)
			}
			out = append(out, telegramch.New(token))
		default:
			return nil, nil, invalidParams("config.channels: unknown channel %q (allowed: slack, telegram, colleagues)", n)
		}
		got = append(got, n)
	}
	if got == nil {
		got = []string{}
	}
	return out, got, nil
}

// profileOf builds a profile whose tools are bridged to the client.
func (s *Server) profileOf(name string, pc ProfileConfig) (profiles.Profile, error) {
	if strings.TrimSpace(name) == "" {
		return profiles.Profile{}, errInvalid("a profile needs a name")
	}
	ts, err := s.bridgeTools(pc.Tools)
	if err != nil {
		return profiles.Profile{}, fmt.Errorf("profile %q: %w", name, err)
	}
	pr := profiles.Profile{Directives: pc.Directives}
	if len(ts) > 0 {
		pr.Tools = func() []tools.Tool { return ts }
	}
	return pr, nil
}

// RegisterToolParams are the params of agent/register_tool.
type RegisterToolParams struct {
	Tool ToolDecl `json:"tool"`
}

func opRegisterTool(_ context.Context, s *Server, p RegisterToolParams) (Empty, error) {
	if err := validateToolDecl(p.Tool); err != nil {
		return Empty{}, err
	}
	s.Agent().RegisterTool(s.bridgeTool(p.Tool))
	return Empty{}, nil
}

// RegisterProfileParams are the params of agent/register_profile.
type RegisterProfileParams struct {
	Name    string        `json:"name"`
	Profile ProfileConfig `json:"profile"`
}

func opRegisterProfile(_ context.Context, s *Server, p RegisterProfileParams) (Empty, error) {
	pr, err := s.profileOf(p.Name, p.Profile)
	if err != nil {
		return Empty{}, err
	}
	if err := s.Agent().RegisterProfile(p.Name, pr); err != nil {
		return Empty{}, errInvalid("%v", err)
	}
	return Empty{}, nil
}

// ProfileNameParams are the params of agent/unregister_profile.
type ProfileNameParams struct {
	Name string `json:"name"`
}

func opUnregisterProfile(_ context.Context, s *Server, p ProfileNameParams) (Empty, error) {
	s.Agent().UnregisterProfile(p.Name)
	return Empty{}, nil
}

// NamesResult is the result of agent/profiles.
type NamesResult struct {
	Names []string `json:"names"`
}

func opProfiles(_ context.Context, s *Server, _ Empty) (NamesResult, error) {
	n := s.Agent().ProfileNames()
	if n == nil {
		n = []string{}
	}
	sort.Strings(n)
	return NamesResult{Names: n}, nil
}

// IDsResult is the result of agent/active_sessions.
type IDsResult struct {
	IDs []string `json:"ids"`
}

func opActiveSessions(_ context.Context, s *Server, _ Empty) (IDsResult, error) {
	ids := s.Agent().ActiveSessions()
	if ids == nil {
		ids = []string{}
	}
	return IDsResult{IDs: ids}, nil
}

// MCPStatusResult is the result of agent/mcp_status.
type MCPStatusResult struct {
	Statuses []mcp.Status `json:"statuses"`
}

func opMCPStatus(_ context.Context, s *Server, _ Empty) (MCPStatusResult, error) {
	st := s.Agent().MCPStatuses()
	if st == nil {
		st = []mcp.Status{}
	}
	return MCPStatusResult{Statuses: st}, nil
}

func opShutdown(_ context.Context, _ *Server, _ Empty) (Empty, error) { return Empty{}, nil }
