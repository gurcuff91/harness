package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gurcuff91/harness/agent/profiles"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/schedule"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/version"
	"github.com/gurcuff91/harness/mcp"
	"github.com/gurcuff91/harness/types"
)

// ── Agent ────────────────────────────────────────────────────────────────

// Agent is a pure factory — it holds global config and spawns Sessions.
// It has zero knowledge of providers, credentials, or which providers are active.
// The caller is responsible for ensuring the provider is active before NewSession().
type Agent struct {
	// subagentStartHookForTest runs at the start of every sub-agent goroutine
	// (a test makes it panic or block). nil in production.
	subagentStartHookForTest func()
	opts                     AgentOptions // original opts — used by Subagent tool to clone
	profiles                 *profiles.Registry
	toolReg                  *tools.Registry
	disallowedTools          []string
	store                    store.SessionStore
	resourceLoader           resources.ResourceLoader // nil = discover nothing
	thinkingLevel            string
	systemPrompt             string
	maxIterations            int
	maxTokens                int          // 0 = resolved from ModelMeta in NewSession
	mcpManager               *mcp.Manager // non-nil only when MCPServers is non-empty; owns MCP subprocesses

	// Memory (nil = no memory). ownsMemory is true when the store was injected
	// via AgentOptions.Memory (this agent must Close it); false when it shares a
	// parent's store (subagent), which must not be closed here.
	memStore   memory.Store
	ownsMemory bool

	// Scheduling. schedStore opens unconditionally (needed for the
	// read-only `harness schedules` listing / HTTP endpoint regardless of
	// EnableScheduler) — the Schedule* TOOLS and schedEngine (which fires
	// due prompts) are both gated on EnableScheduler; see scheduleAdapter().
	schedStore  *schedule.Store
	schedEngine *schedule.Engine

	// activeSessions tracks every live session (created via NewSession or
	// ResumeSession, removed on Close) by id. The scheduler routes a fired prompt
	// to the session named by the schedule's owner; a frontend reaches whatever
	// output it wants by subscribing to that session's events. Guarded by sessMu.
	sessMu         sync.Mutex
	activeSessions map[string]*Session
	// adm is the cached Admin (see Admin()).
	admOnce sync.Once
	adm     *admin.Admin
	// openMu/opening serialize the slow path of ResumeSession per session id.
	openMu  sync.Mutex
	opening map[string]*openLock
	// onRegisterForTest, when set, runs on every registerSession (tests).
	onRegisterForTest func()

	// Graceful shutdown (idempotent Close).
	closeOnce sync.Once
	closeErr  error
}

// AgentOptions configures a new Agent.
type AgentOptions struct {
	// ── Thinking ─────────────────────────────────────────────────────────
	ThinkingLevel string // "disable"|"low"|"medium"|"high"|"xhigh"|"max"

	// ── Behavior ─────────────────────────────────────────────────────────
	SystemPrompt  string   // base system prompt for all sessions; precedence: this > SYSTEM.md > built-in default
	Directives    []string // extra instruction blocks appended to the system prompt (e.g. a frontend's capabilities)
	MaxIterations int      // max ReAct iterations per turn — default: 50 (defaultMaxIterations)
	MaxTokens     int      // max output tokens — default: model's MaxTokens from ModelMeta

	// ── Profiles ─────────────────────────────────────────────────────────
	// Profiles are named bundles of directives and tools that SOME sessions
	// can be bound to (SessionWithEnabledProfiles) — see package profiles. More can be
	// added, replaced or removed live with Agent.RegisterProfile.
	Profiles map[string]profiles.Profile

	// ── Tools ────────────────────────────────────────────────────────────
	Tools           []tools.Tool // additional tools (defaults always included)
	DisallowedTools []string     // tool names to exclude — empty = all allowed
	// MCPServers are the MCP servers this agent spawns/connects (root agent
	// only), keyed by server name — their tools register as
	// mcp__<name>__<tool>. Presence is the switch: empty (the default) = no
	// MCP at all. The agent owns the resulting processes/connections:
	// Agent.Close terminates them. Use mcp.ServersFromSettings() for the ones
	// configured via `harness mcp add`, your own map, or both merged.
	MCPServers map[string]types.MCPServer

	// ── Infrastructure (optional) ────────────────────────────────────────
	Store store.SessionStore // default: in-memory
	// ResourceLoader discovers SYSTEM.md, AGENTS.md and skills for each session's
	// cwd. nil = discover NOTHING; pass resources.File() to discover on disk (what
	// the harness CLI does). The loader is stateless and shared by every session.
	ResourceLoader resources.ResourceLoader

	// Memory is the project-scoped persistent memory backend. Its presence is
	// the on/off switch: nil (the default) = no memory at all — no Memo* tools,
	// no "## Memory" prompt block. The agent owns the store: Agent.Close closes
	// it. Use memory.OpenSQLite for the default SQLite backend
	// (~/.harness/agent/memory.db), or any custom memory.Store.
	Memory memory.Store
	// sharedMemory lets a subagent reuse its parent's store without owning it
	// (never closed by the subagent). Unexported: only agent.go sets it
	// (subagent path); SDK callers use Memory.
	sharedMemory memory.Store
	// EnableScheduler turns on cron-scheduled prompts: the Schedule* management
	// tools AND the engine that fires due prompts. The agent owns both. A
	// frontend marks one session as the scheduler target (SetScheduledSession);
	// only one agent should enable this so prompts don't fire twice.
	EnableScheduler bool

	// EnableWebSearch registers the built-in WebSearch tool. The tool owns its
	// own HTTP client and uses the active minimax provider's API key — if the
	// provider isn't connected, the tool returns a single actionable error at
	// call time, so it's safe to enable unconditionally without a pre-flight
	// provider check. Mirrors the opt-in style of MCPServers/Memory for "extra" tools.
	EnableWebSearch bool

	// Terminal names the terminal this agent's sessions run in (e.g.
	// "Ghostty", or a workspace terminal with its address), reported by SessionInfo. Empty for an agent that has
	// no terminal of its own (a server, an SDK embedder, a bot): it is then
	// omitted rather than guessed from the launcher's environment.
	Terminal string

	// Channels are the external-conversation implementations (Slack, …) a
	// session can be bound to with SessionWithChannels. Registering one opens
	// nothing: a channel listens only for the sessions bound to it. The agent
	// does not own them beyond that (each session stops its own listeners).
	Channels []Channel
}

// defaultMaxIterations is the fallback used when AgentOptions.MaxIterations
// isn't set. It's the SDK/one-shot-command default — generous enough for a
// real multi-step task without being unbounded; interactive frontends (TUI,
// serve, Telegram) that expect longer, more complex work override it higher
// (see internal/cli/agent.go), and subagents cap it lower via
// subagentMaxIterations (see the Subagent tool wiring below).
const defaultMaxIterations = 50

// minMaxIterations/maxMaxIterations bound any EXPLICIT max_iterations value
// — AgentOptions.MaxIterations when set (0/negative still falls back to
// defaultMaxIterations, untouched by this bound) and Session.SetMaxIterations'
// per-session runtime override. 1 as a floor (not higher) deliberately keeps
// fetchSummarizeMaxIterations (5, an internal single-turn budget) valid
// without needing a special case.
const (
	minMaxIterations = 1
	maxMaxIterations = 1000
)

// validateMaxIterations reports whether n is a legal explicit max_iterations
// value — shared by agent.New (AgentOptions.MaxIterations) and
// Session.SetMaxIterations so both enforce the exact same bound.
func validateMaxIterations(n int) error {
	if n < minMaxIterations || n > maxMaxIterations {
		return fmt.Errorf("max_iterations must be between %d and %d (got %d)", minMaxIterations, maxMaxIterations, n)
	}
	return nil
}

// subagentMaxIterations is the DEFAULT iteration budget for a subagent —
// used when the Subagent tool's caller doesn't request an override via its
// optional 'max_iterations' param (see tools.subagentMinIterations/
// subagentMaxIterationsCeiling, and the Subagent executor closure below). A
// subagent is a focused, delegated task (see subagentSystemPrompt), not the
// primary agent driving a long, multi-part session — it shouldn't need as
// much room as a parent running with interactiveMaxIterations (120) by
// default, and capping it means a runaway subagent gets cut off (with the
// usual progress-summary fallback) well before it burns through a comparable
// budget without the parent knowing until it finally returns. Deliberately
// NOT capped by the parent's OWN maxIterations (min() against it used to
// happen here, removed): the model can explicitly ask for a larger budget,
// up to tools.subagentMaxIterationsCeiling (200), for a genuinely large or
// multi-step delegated task — independent of however modest the parent
// session's own interactive budget happens to be.
const subagentMaxIterations = 50

// fetchSummarizeMaxIterations caps the ephemeral sub-agent Fetch's
// FetchSummarizer spins up to condense content. It gets NO tools at all (see
// the call site's DisallowedTools) and a single-turn job — read the content,
// answer per the instruction — so it needs far less room than a general
// Subagent call (which may explore, read files, run commands across many
// iterations). A small budget here just means a model that somehow keeps
// "thinking" without producing a final answer gets cut off promptly instead
// of burning an oversized iteration budget on a task that should resolve in
// one turn.
const fetchSummarizeMaxIterations = 5

// New creates a new Agent. Never fails — provider is resolved per session.
func New(opts AgentOptions) *Agent {
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = defaultMaxIterations
	} else if opts.MaxIterations > maxMaxIterations {
		// New() never fails (documented contract) — an explicit value ABOVE
		// the ceiling is clamped silently rather than rejected. Below the
		// floor can't happen here: any value <=0 already took the default
		// branch above, and minMaxIterations is 1, so every remaining
		// positive value already satisfies the floor.
		opts.MaxIterations = maxMaxIterations
	}
	if opts.ThinkingLevel == "" {
		// Fall back to the user's configured level, then to "off". Doing this in
		// New — the single entry point for every caller (CLI, TUI, SDK) — lets the
		// SDK facade stay a thin zero-value pass-through while still yielding a
		// sensible default.
		if lvl := config.GetSettingsManager().ThinkingLevel(); lvl != "" {
			opts.ThinkingLevel = lvl
		} else {
			opts.ThinkingLevel = "off"
		}
	}
	if opts.Store == nil {
		// Default: file-backed store in ~/.harness/agent/sessions/
		// Falls back to in-memory if filesystem is unavailable
		if fs, err := store.NewFileStore(""); err == nil {
			opts.Store = fs
		} else {
			opts.Store = store.NewInMemoryStore()
		}
	}

	// No built-ins are seeded here anymore: Fetch moved to buildSessionTools
	// alongside Bash/Read/Write/Edit — it now needs a per-session
	// FetchSummarizer closure (reads the session's CURRENT model, like
	// Subagent's executor does), which doesn't exist yet at this point in
	// New(). This registry starts empty and only ever grows from MCP tools
	// and AgentOptions.Tools below.
	reg := tools.NewRegistry()

	// Connect configured MCP servers eagerly (root agent only). Their tools are
	// registered alongside the built-ins and shared by every session. Failures
	// degrade silently — recorded in the manager's Statuses(), never logged to
	// stdout (which would corrupt the TUI).
	var mcpMgr *mcp.Manager
	if len(opts.MCPServers) > 0 {
		mcpMgr = mcp.NewManager()
		for _, t := range mcpMgr.Start(context.Background(), opts.MCPServers) {
			reg.Register(t)
		}
	}

	// Additional tools (built-ins always included). Subagents receive the
	// parent's MCP tools here without spawning their own processes.
	for _, t := range opts.Tools {
		reg.Register(t)
	}

	profs := profiles.NewRegistry()
	for name, p := range opts.Profiles {
		_ = profs.Register(name, p) // a blank name cannot be registered: skipped
	}
	a := &Agent{
		opts:            opts,
		profiles:        profs,
		toolReg:         reg,
		disallowedTools: opts.DisallowedTools,
		store:           opts.Store,
		resourceLoader:  opts.ResourceLoader,
		thinkingLevel:   opts.ThinkingLevel,
		systemPrompt:    opts.SystemPrompt,
		maxIterations:   opts.MaxIterations,
		maxTokens:       opts.MaxTokens,
		mcpManager:      mcpMgr,
		activeSessions:  make(map[string]*Session),
	}

	// Scheduling: the agent always opens the store — Schedules() serves the
	// read-only `harness schedules` listing / HTTP endpoint regardless of
	// EnableScheduler, and the schedules may be shared across several
	// harness processes on this machine. The Schedule* MANAGEMENT TOOLS and
	// the engine that fires due prompts both require EnableScheduler (see
	// scheduleAdapter()) — an instance without it neither manages nor
	// executes schedules, avoiding schedules created from an instance that
	// would never run them. Subagents pass EnableScheduler=false and also
	// disallow the Schedule* tools explicitly (belt and suspenders).
	if st, err := schedule.Open(); err == nil {
		a.schedStore = st
		if opts.EnableScheduler {
			a.schedEngine = schedule.NewEngine(st, a.fireScheduledPrompt)
			a.schedEngine.Start(context.Background())
		}
	}

	// Memory: a subagent shares its parent's store (sharedMemory, not owned);
	// otherwise the injected store, if any, is owned and closed by this agent.
	// The agent never opens a backend itself — no store, no memory.
	if opts.sharedMemory != nil {
		a.memStore = opts.sharedMemory
	} else if !isNilStore(opts.Memory) {
		a.memStore = opts.Memory
		a.ownsMemory = true
	}

	return a
}

// subagentDisallowedTools is the blocklist every Subagent sub-agent gets.
// Base restrictions, regardless of readonly: no recursion (Subagent), READ-ONLY
// memory (MemoWrite/MemoDelete blocked, MemoSearch kept — only the parent
// curates what persists, avoiding noisy/conflicting writes from ephemeral
// sub-agents sharing its store), and no schedule management (Schedule*). When
// readonly is requested, Write and Edit are appended on top — an extra
// STRUCTURAL layer beyond whatever the caller's own prompt already tells the
// sub-agent not to do (see subagentInput.Readonly's doc comment in
// tools/subagent.go for the exact guarantee: this blocks the two structured
// file-editing tools, not Bash, which can still modify files if the sub-agent
// disobeys its instructions anyway).
func subagentDisallowedTools(readonly bool) []string {
	disallowed := []string{
		tools.ToolSubagent, tools.ToolMemoWrite, tools.ToolMemoDelete,
		tools.ToolSchedule, tools.ToolScheduleList, tools.ToolScheduleDelete,
	}
	if readonly {
		disallowed = append(disallowed, tools.ToolWrite, tools.ToolEdit)
	}
	return disallowed
}

// isNilStore reports whether s is nil OR a typed nil pointer wrapped in the
// interface — e.g. the *memory.SQLiteStore a failed memory.OpenSQLite returns,
// passed along without checking its error. A typed nil compares != nil, so
// without this the agent would believe it has memory and fail on first use;
// treating it as "no memory" keeps the documented degrade-to-off behavior.
func isNilStore(s memory.Store) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// fireScheduledPrompt is the engine callback: it routes the due prompt to the
// session named by the schedule's owner — ONLY if that session is already
// active in THIS instance. If it isn't, the prompt is dropped silently (the
// engine still records the run via RecordRun, so no catch-up pileup). This
// is deliberate, not a gap: an instance should fire only into sessions IT
// itself has live — never resurrect an arbitrary session from disk on the
// engine's own initiative: whoever opens the session (the TUI, a client of
// serve) keeps it live to receive its schedules.
//
// A schedule created from a channel conversation the session still listens to
// fires back INTO it (its origin), so the channel publishes the reply; else it
// fires as the session's own (no origin). Either way the prompt is wrapped in
// <scheduled slug="…">: that wrapper, not an origin, is what says a prompt was
// fired by the scheduler (the model reads it; the TUI shows ◷ for it).
//
// In practice schedEngine.evaluate (see agent/schedule/engine.go) already
// filters to only THIS engine's watched sessions before ever calling this
// function, so owner not resolving here would mean a session closed in the
// narrow window between evaluate's check and this call — a legitimate,
// harmless drop, not the routine case it used to be before that filter
// existed. Several harness processes, each running --with-scheduler and each
// watching only their own live sessions, is now a safe, intended
// deployment shape — see Engine's own doc comment for the full story.
func (a *Agent) fireScheduledPrompt(sc schedule.Schedule) {
	sess := a.resolveScheduledSession(sc.Owner)
	if sess == nil {
		return
	}
	text := wrapScheduled(sc.Slug, sc.Prompt)
	if sc.Origin != "" && sess.channels.listensTo(sc.Origin) {
		// Created from a channel conversation the session still listens to:
		// the turn goes back there, and the channel publishes its reply.
		sess.Prompt(context.Background(), text, PromptWithOrigin(sc.Origin))
		return
	}
	sess.Prompt(context.Background(), text)
}

// wrapScheduled marks a fired schedule's prompt, so the model knows nobody is
// waiting at the other end and no person sent it — also when its turn is
// routed to a channel conversation.
func wrapScheduled(slug, prompt string) string {
	return `<scheduled slug="` + slug + `">` + "\n" + strings.TrimSpace(prompt) + "\n</scheduled>"
}

// scheduleOrigin is the origin a schedule created now records: the current
// turn's, when it is a conversation one of the session's channels owns
// durably (a Slack conversation, a Telegram chat, a colleague's QUESTION —
// whose answer then goes back to that colleague each time it fires); ""
// otherwise (the person at the TUI, another schedule, a colleague's reply).
func (s *Session) scheduleOrigin() string {
	o, _ := s.turnOrigin.Load().(string)
	if !s.channels.listensTo(o) {
		return ""
	}
	return o
}

// resolveScheduledSession returns the active session a fired schedule targets —
// the one named by owner — or nil if it isn't active (the prompt is dropped).
// Every schedule carries its owner (the creating session's id); an empty owner
// only comes from a stale pre-owner schedule, which we simply don't run.
func (a *Agent) resolveScheduledSession(owner string) *Session {
	if owner == "" {
		return nil
	}
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	return a.activeSessions[owner]
}

// registerSession adds a live session to the active set (keyed by id). Called
// by NewSession/ResumeSession/ForkSession. unregisterSession removes it
// (called on Close). Also tells schedEngine (nil unless EnableScheduler) to
// start/stop watching this session's id — see Engine.AddSession's doc
// comment for why: this is what makes it safe for several harness processes
// to each run their own engine over the same shared schedules, each
// firing only into the sessions IT itself has live.
func (a *Agent) registerSession(s *Session) {
	a.sessMu.Lock()
	a.activeSessions[s.id] = s
	a.sessMu.Unlock()
	if a.onRegisterForTest != nil {
		a.onRegisterForTest()
	}
	if a.schedEngine != nil {
		a.schedEngine.AddSession(s.id)
	}
}

func (a *Agent) unregisterSession(id string) {
	a.sessMu.Lock()
	delete(a.activeSessions, id)
	a.sessMu.Unlock()
	if a.schedEngine != nil {
		a.schedEngine.DelSession(id)
	}
}

// scheduleAdapter exposes the agent's schedule store to the Schedule* tools.
// Returns nil (no tools registered) unless EnableScheduler is set — the
// store itself is always opened (Schedules() still serves the read-only
// `harness schedules` listing / HTTP endpoint regardless), but the
// Schedule/ScheduleList/ScheduleDelete TOOLS are only handed to the model
// when THIS instance is the one actually running the engine that fires
// them. Previously the tools were always registered whenever the store
// opened, letting a model create/manage schedules from an instance that
// would never execute them unless some OTHER --with-scheduler instance happened
// to be running — confusing and easy to end up with schedules that never
// fire. One agent, one engine, one set of tools: they now travel together.
func (a *Agent) scheduleAdapter() tools.ScheduleStore {
	if a.schedStore == nil || !a.opts.EnableScheduler {
		return nil
	}
	return schedule.NewToolAdapter(a.schedStore)
}

// schedules returns the agent's schedule store (nil if unavailable).
func (a *Agent) schedules() *schedule.Store { return a.schedStore }

// Admin returns the admin layer bound to this agent: its session store, its
// memory store and its ActiveSessions (so deleting or renaming a session open
// here is refused). Built once and cached. The stores belong to the agent:
// closing the Admin does not close them.
func (a *Agent) Admin() *admin.Admin {
	a.admOnce.Do(func() {
		a.adm = admin.New(
			admin.WithSessionStore(a.store),
			admin.WithMemory(a.memStore),
			admin.WithActiveSessions(a.ActiveSessions),
		)
	})
	return a.adm
}

// Options returns the original configuration — used by the Subagent tool to clone.
func (a *Agent) Options() AgentOptions {
	return a.opts
}

// webSearchBackends returns the list of currently-active search backends
// in the dispatch order the WebSearch tool expects (minimax first,
// ollama-cloud second, synthetic third). A provider is included only when it's both IsActive
// AND ResolveCredentials returns a non-empty API key — same gate the
// provider registry already uses to decide model availability.
//
// Backends are built fresh on each call so a freshly-connected provider
// (or a freshly-rotated key, via the credentials chain) starts being
// used immediately, without restarting the agent.
func (a *Agent) webSearchBackends() []tools.SearchBackend {
	providers.EnsureRegistry()
	want := []struct {
		name string
		url  string
	}{
		{"minimax", "https://api.minimax.io/v1/coding_plan/search"},
		{"ollama-cloud", "https://ollama.com/api/web_search"},
		{"synthetic", "https://api.synthetic.new/v2/search"},
	}
	out := make([]tools.SearchBackend, 0, len(want))
	for _, w := range want {
		var p providers.Provider
		for _, candidate := range providers.All() {
			if candidate.Name() == w.name {
				p = candidate
				break
			}
		}
		if p == nil {
			continue
		}
		if !p.IsActive() {
			continue
		}
		creds, err := p.ResolveCredentials()
		if err != nil || creds.APIKey == "" {
			continue
		}
		out = append(out, tools.SearchBackend{
			Name: w.name,
			URL:  w.url,
			Key:  creds.APIKey,
		})
	}
	return out
}

// webSearchLookup adapts webSearchBackends() to the ProviderLookup
// interface the tool consumes.
//
// IMPORTANT: this must NOT snapshot the backend list — buildSessionTools
// (and therefore this function) runs once, at session-creation time, and
// the resulting Tool is registered for the session's whole lifetime. A
// captured snapshot would keep answering with the backends that were
// active at session start, even after the user disconnects both
// minimax/ollama-cloud mid-session (or connects a fresh one). Returning a
// live closure instead means every Execute call re-walks the provider
// registry and re-resolves credentials at THAT moment — connect/disconnect
// takes effect on the very next WebSearch call, no session restart needed.
func (a *Agent) webSearchLookup() tools.ProviderLookup {
	return searchBackendsLookupFunc(a.webSearchBackends)
}

// searchBackendsLookupFunc adapts a plain function to ProviderLookup so
// webSearchLookup can hand the tool a live callback instead of a frozen
// struct — see webSearchLookup's doc comment for why this matters.
type searchBackendsLookupFunc func() []tools.SearchBackend

func (f searchBackendsLookupFunc) ActiveSearchBackends() []tools.SearchBackend {
	return f()
}

// RegisterTool adds a tool to the agent's registry so all future sessions
// created by this agent include it. Must be called before NewSession/ResumeSession.
// Idempotent: re-registering the same name replaces the previous entry.
func (a *Agent) RegisterTool(t tools.Tool) {
	a.toolReg.Register(t)
}

// MCPTools returns the agent's MCP tools, for sharing with subagents (which set
// no MCPServers and receive these via AgentOptions.Tools, reusing the
// parent's live MCP processes). Nil when MCP is disabled.
func (a *Agent) MCPTools() []tools.Tool {
	if a.mcpManager == nil {
		return nil
	}
	return a.mcpManager.Tools()
}

// MCPStatuses reports the connection state of each configured MCP server. Nil
// when MCP is disabled. Exposed (e.g. via the HTTP API) so clients can render
// status without the manager writing to stdout.
func (a *Agent) MCPStatuses() []mcp.Status {
	if a.mcpManager == nil {
		return nil
	}
	return a.mcpManager.Statuses()
}

// Close releases agent-owned resources: it terminates MCP subprocesses and
// closes the memory store it was given (AgentOptions.Memory). Only the root agent should be closed — subagents
// are ephemeral, have no MCP manager, and merely share the parent's memory
// store (which they must not close). Idempotent (sync.Once) and nil-safe; both
// resources are released even if one fails.
func (a *Agent) Close() error {
	a.closeOnce.Do(func() {
		var errs []error
		// Stop every listener and release every claim this agent's sessions hold.
		a.sessMu.Lock()
		live := make([]*Session, 0, len(a.activeSessions))
		for _, s := range a.activeSessions {
			live = append(live, s)
		}
		a.sessMu.Unlock()
		for _, s := range live {
			s.shutdownSubagents() // no sub-agent may outlive the agent's stores
			s.closeChannels()
		}
		if a.mcpManager != nil {
			if err := a.mcpManager.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if a.memStore != nil && a.ownsMemory {
			if err := a.memStore.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if a.schedEngine != nil {
			a.schedEngine.Stop()
		}
		if a.store != nil {
			if err := a.store.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}

// SessionOption configures one session as it's created (NewSession) or
// reopened (ResumeSession) — scoped to that session object only, never to
// the Agent's other sessions, and never persisted.
type SessionOption func(*sessionConfig)

type sessionConfig struct {
	// directives and tools are NOT options: they are what the session's enabled
	// profiles and its own directives contribute (filled by profilesFor). The
	// agent configures the tools; a session chooses channels, profiles and its
	// own instruction text.
	directives []string
	tools      []tools.Tool
	// own: the text given with SessionWithDirectives (never persisted).
	own      []string
	profiles []string
	// setProfile: SessionWithEnabledProfiles was passed, even with no names. It is the
	// difference between "leave the session's profiles as they are" and "none".
	setProfile bool
	channels   []types.InputChannel
	// err: an option that could not be validated on its own (a blank profile
	// name); NewSession/ResumeSession/ForkSession return it before opening.
	err error
}

// SessionWithEnabledProfiles binds the session to the named profiles (package
// profiles; defined with AgentOptions.Profiles or Agent.RegisterProfile): their
// directives and tools apply to it, and the names are stored in the session, so
// every later open — by any frontend — applies them again. It works on
// NewSession, ResumeSession and ForkSession, and it REPLACES, like a PATCH:
//
//   - not passed: the session keeps the profiles it has (a new one has none; a
//     fork keeps its parent's);
//   - one or more names: exactly those;
//   - passed with no names, SessionWithEnabledProfiles(): none.
//
// A name that is not registered fails NewSession (and ResumeSession/ForkSession
// when passed here): nothing is opened. A STORED name that is no longer
// registered is skipped when a session opens — it stays usable, the name is
// kept for when it is registered again, and Session.MissingProfiles reports
// it. A blank name is an error.
func SessionWithEnabledProfiles(names ...string) SessionOption {
	return func(c *sessionConfig) {
		c.setProfile = true
		c.profiles = append([]string(nil), names...)
		for _, n := range names {
			if err := profiles.ValidName(n); err != nil && c.err == nil {
				c.err = err
			}
		}
	}
}

// SessionWithDirectives adds instruction text to THIS session's system prompt
// only — for text that is particular to one session (its working tree, its
// task), where a profile (shared text and tools, registered once by name) is
// the wrong tool. It works on NewSession, ResumeSession, ForkSession and
// ImportSession. Order in the prompt: the agent's directives, the enabled
// profiles', these, then the channels'. Blank blocks are dropped and a block
// identical to an earlier one appears once.
//
// Never persisted: they live in the open session, so whoever reopens it passes
// them again (a fork does not inherit its parent's, an import does not bring
// the export's). A resume that returns an already-open session leaves its
// prompt as it is. Profiles that change while the session is open keep them.
func SessionWithDirectives(directives ...string) SessionOption {
	return func(c *sessionConfig) { c.own = append(c.own, directives...) }
}

func applySessionOptions(opts []SessionOption) sessionConfig {
	var c sessionConfig
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	return c
}

// NewSession creates a fresh session for the given working directory and model.
// model is required in "provider/model" format (e.g. "anthropic/claude-sonnet-4").
// Returns error if the provider is not active or the model doesn't exist.
// opts configure this session only (see SessionWithDirectives).
func (a *Agent) NewSession(cwd, model string, opts ...SessionOption) (*Session, error) {
	cfg := applySessionOptions(opts)
	// The profiles it is bound to (none unless asked), checked BEFORE anything
	// is created: an unknown one fails the open.
	cfgOwn := cfg.own
	profileNames, cfg, missingProfiles, err := a.profilesFor(nil, cfg)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	// Resolve provider — validates active + model exists
	provider, modelID, err := providers.Resolve(model)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}

	// Output ceiling: the user's AgentOptions.MaxTokens when set (always
	// wins), else the catalog value capped at defaultMaxOutputTokens.
	maxTokens := sessionMaxTokens(a.maxTokens, provider.ModelMeta(modelID))

	// Resources
	res, err := a.loadResources(cwd)
	if err != nil {
		return nil, fmt.Errorf("load resources: %w", err)
	}

	now := time.Now()
	// a.thinkingLevel is guaranteed non-empty: agent.New resolves it to the
	// configured level or "off" (the single entry point for every caller), so
	// no fallback is needed here.
	// The id is generated first so the session's Schedule tool can capture it as
	// the owner for any schedules it creates.
	sessionID := uuid.New().String()
	// sess is filled in below, right after newSession — see buildSessionTools'
	// sessRef doc comment for why the Subagent tool needs this indirection
	// instead of the plain "model" string.
	var sess *Session
	chans, chDirectives, chTools, err := a.prepareChannels(sessionID, cwd, a.withSessionChannels(cfg.channels), cfg.tools)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	sessionTools, tl := a.buildSessionTools(sessionID, cwd, &sess, res, append(append([]tools.Tool(nil), cfg.tools...), chTools...)...)
	systemPrompt, pl := a.buildSystemPrompt(cwd, res, append(append([]string(nil), cfg.directives...), chDirectives...)...)

	meta := store.SessionMeta{
		ID:           sessionID,
		CWD:          cwd,
		Name:         defaultSessionName(now),
		Model:        model,
		Thinking:     a.thinkingLevel,
		Profiles:     profileNames,
		CreatedAt:    now,
		LastActiveAt: now,
	}
	storeInst, err := store.CreateSession(a.store, meta)
	if err != nil {
		chans.release()
		return nil, fmt.Errorf("create store: %w", err)
	}

	sess = newSession(storeInst,
		provider, modelID, a.thinkingLevel,
		sessionTools, tl, systemPrompt, pl,
		a.maxIterations, maxTokens,
		res.Skills, skillReader(res.Skills),
		a.memStore != nil)
	sess.agent = a
	sess.ownDirectives = append([]string(nil), cfgOwn...)
	sess.missingProfiles.Store(missingProfiles)
	if err := sess.attachChannels(chans); err != nil {
		_ = storeInst.Close()
		_ = a.store.DeleteSession(sessionID)
		return nil, fmt.Errorf("new session: %w", err)
	}
	a.registerSession(sess)
	return sess, nil
}

// ResumeSession reopens an existing session, fully restoring its state. If the
// session is already active (live in the agent's registry), it returns the
// existing handle without reloading from disk — making this idempotent and safe
// to call from multiple paths (frontend reconnect, scheduler auto-resume, …).
//
// opts configure the reopened session (see SessionWithDirectives). They are
// IGNORED on that already-active fast path: the live session keeps the
// system prompt it was opened with, so two frontends sharing a session never
// overwrite each other's prompt.
func (a *Agent) ResumeSession(sessionID string, opts ...SessionOption) (*Session, error) {
	cfg := applySessionOptions(opts)
	if cfg.err != nil {
		return nil, fmt.Errorf("resume session: %w", cfg.err)
	}
	// Fast path: already active — return the live handle.
	a.sessMu.Lock()
	if live, ok := a.activeSessions[sessionID]; ok {
		a.sessMu.Unlock()
		// Profiles are the one option that DOES apply to an already-open
		// session (the rest are ignored here): passing them rebinds it, and it
		// picks them up at its next turn boundary.
		if cfg.setProfile {
			if err := a.checkProfileNames(cfg.profiles); err != nil {
				return nil, fmt.Errorf("resume session: %w", err)
			}
			if err := a.setSessionProfiles(sessionID, cfg.profiles); err != nil {
				return nil, fmt.Errorf("resume session: %w", err)
			}
			if err := a.reconfigureSession(sessionID); err != nil {
				return nil, fmt.Errorf("resume session: %w", err)
			}
		}
		if len(cfg.channels) > 0 && !live.hasChannels(cfg.channels) {
			// Channels are bound when a session opens; an already-open session
			// is not rebound behind its owner's back (and an explicit channel
			// is never silently ignored).
			return nil, fmt.Errorf("resume session: %w: session %s is already open; close it to bind channels", ErrChannelInvalid, sessionID)
		}
		return live, nil
	}
	a.sessMu.Unlock()

	// Slow path, serialized per id: concurrent resumes of the same closed
	// session yield ONE live Session (the later ones wait, then take the fast
	// path). Resumes of other ids are not blocked. sessMu is never held here.
	unlockOpen := a.lockOpen(sessionID)
	a.sessMu.Lock()
	_, raced := a.activeSessions[sessionID]
	a.sessMu.Unlock()
	if raced {
		unlockOpen()
		return a.ResumeSession(sessionID, opts...)
	}
	defer unlockOpen()
	return a.resumeStored(sessionID, cfg)
}

// lockOpen serializes the opening of one session id; the returned func
// releases it. The per-id entry is dropped when nobody holds or waits on it.
func (a *Agent) lockOpen(id string) func() {
	a.openMu.Lock()
	if a.opening == nil {
		a.opening = make(map[string]*openLock)
	}
	l := a.opening[id]
	if l == nil {
		l = &openLock{}
		a.opening[id] = l
	}
	l.refs++
	a.openMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		a.openMu.Lock()
		if l.refs--; l.refs == 0 {
			delete(a.opening, id)
		}
		a.openMu.Unlock()
	}
}

type openLock struct {
	mu   sync.Mutex
	refs int
}

// resumeStored opens a stored session that is not live (caller holds the
// per-id open lock).
func (a *Agent) resumeStored(sessionID string, cfg sessionConfig) (*Session, error) {
	storeInst, err := store.OpenSession(a.store, sessionID)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if storeInst == nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	meta := storeInst.Meta()

	// The profiles it ends up bound to: the stored ones, or the ones asked for
	// (which REPLACE them and are saved). A stored one that is no longer
	// registered is skipped and reported, never an error.
	cfgOwn := cfg.own
	profileNames, cfg, missingProfiles, err := a.profilesFor(meta.Profiles, cfg)
	if err != nil {
		_ = storeInst.Close()
		return nil, fmt.Errorf("resume session: %w", err)
	}
	if cfg.setProfile {
		meta.Profiles = profileNames
		if err := storeInst.UpdateMeta(meta); err != nil {
			_ = storeInst.Close()
			return nil, fmt.Errorf("resume session: save profiles: %w", err)
		}
	}

	// Restore provider+model. Models are scoped per session, so the saved one
	// comes first; if its provider is gone (disconnected, credentials removed, model
	// retired) the session is NOT stranded — it reopens on the harness's default
	// model, and the switch is persisted and reported (Session.ModelFallback).
	provider, modelID, fallback, err := resolveSessionModel(meta.Model)
	if err != nil {
		return nil, fmt.Errorf("resume session: %w", err)
	}
	if fallback != nil {
		meta.Model = fallback.To
		if err := storeInst.UpdateMeta(meta); err != nil {
			return nil, fmt.Errorf("resume session: persist model fallback: %w", err)
		}
	}

	thinkingLvl := a.thinkingLevel
	if meta.Thinking != "" {
		thinkingLvl = meta.Thinking
	}

	// A per-session SetMaxIterations override survives resume the same way
	// thinkingLvl does above — meta.MaxIterations is 0 (omitempty) unless
	// the session explicitly set one, in which case it wins over the owning
	// Agent's own default.
	maxIterations := a.maxIterations
	if meta.MaxIterations > 0 {
		maxIterations = meta.MaxIterations
	}

	maxTokens := sessionMaxTokens(a.maxTokens, provider.ModelMeta(modelID))

	cwd := meta.CWD
	res, _ := a.loadResources(cwd) // a failing loader leaves the session without context, not unopenable
	skills, readSkill := res.Skills, skillReader(res.Skills)

	var sess *Session
	chans, chDirectives, chTools, err := a.prepareChannels(meta.ID, cwd, a.withSessionChannels(cfg.channels), cfg.tools)
	if err != nil {
		_ = storeInst.Close()
		return nil, fmt.Errorf("resume session: %w", err)
	}
	resumeTools, tl := a.buildSessionTools(meta.ID, cwd, &sess, res, append(append([]tools.Tool(nil), cfg.tools...), chTools...)...)
	resumePrompt, pl := a.buildSystemPrompt(cwd, res, append(append([]string(nil), cfg.directives...), chDirectives...)...)
	sess = newSession(storeInst,
		provider, modelID, thinkingLvl,
		resumeTools, tl, resumePrompt, pl,
		maxIterations, maxTokens,
		skills, readSkill,
		a.memStore != nil)
	sess.agent = a
	sess.ownDirectives = append([]string(nil), cfgOwn...)
	sess.modelFallback = fallback
	sess.missingProfiles.Store(missingProfiles)
	if err := sess.attachChannels(chans); err != nil {
		_ = storeInst.Close()
		return nil, fmt.Errorf("resume session: %w", err)
	}
	a.registerSession(sess)
	return sess, nil
}

// reconfigureSession rebuilds a LIVE session's system prompt and tool
// registry from the agent's configuration, the profiles it is bound to
// (registered now) and its channels, and applies them at the session's next
// turn boundary, never mid-turn; an idle session applies them immediately. This
// is how a session that is already active picks up a profile registered,
// replaced or removed after it was opened, or a rebind (ResumeSession with
// SessionWithEnabledProfiles). Private on purpose: nobody outside the agent
// should force a reconfiguration. Returns an error if the session isn't active.
func (a *Agent) reconfigureSession(sessionID string) error {
	a.sessMu.Lock()
	sess, ok := a.activeSessions[sessionID]
	a.sessMu.Unlock()
	if !ok {
		return fmt.Errorf("session %s is not active", sessionID)
	}
	_, cfg, missing, err := a.profilesFor(sess.store.Meta().Profiles, sessionConfig{own: sess.ownDirectives})
	if err != nil {
		return err
	}
	sess.missingProfiles.Store(missing)
	cwd := sess.CWD()
	res, _ := a.loadResources(cwd)
	sessRef := sess
	// The session's channels keep their tools and directive: they belong to
	// how the session was opened.
	if sc := sess.channels; sc != nil {
		cfg.tools = append(append([]tools.Tool(nil), cfg.tools...), sc.tools...)
		cfg.directives = append(append([]string(nil), cfg.directives...), sc.directives...)
	}
	reg, tl := a.buildSessionTools(sessionID, cwd, &sessRef, res, cfg.tools...)
	prompt, pl := a.buildSystemPrompt(cwd, res, cfg.directives...)
	sess.queueReconfig(&sessionReconfig{systemPrompt: prompt, pl: pl, tools: reg, tl: tl})
	return nil
}

// setSessionProfiles persists the names of the profiles bound to a session
// (store.SessionMeta.Profiles), replacing them — nil or empty removes them all.
// It only changes the STORED names, on an active or an inactive session alike:
// an active one applies them at its next reconfigureSession (ResumeSession and
// SessionWithEnabledProfiles do both).
func (a *Agent) setSessionProfiles(sessionID string, profiles []string) error {
	a.sessMu.Lock()
	sess, ok := a.activeSessions[sessionID]
	a.sessMu.Unlock()
	if ok {
		meta := sess.store.Meta()
		meta.Profiles = append([]string(nil), profiles...)
		return sess.store.UpdateMeta(meta)
	}
	st, err := store.OpenSession(a.store, sessionID)
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("session %s not found", sessionID)
	}
	meta := st.Meta()
	meta.Profiles = append([]string(nil), profiles...)
	return st.UpdateMeta(meta)
}

// ── Session management ───────────────────────────────────────────────────

// ForkSession creates a new session that is an exact copy of sessionID at this
// moment: same CWD, model, thinking, compaction state, stats, and full message
// history. The fork gets a new ID and fresh timestamps. The parent is unchanged.
// Returns ErrBusy (via the store layer) if the parent turn is in flight.
// Session-scoped directives and tools (SessionWithDirectives/SessionWithTools)
// are not persisted, so the fork does not inherit the parent's — only the
// Agent-wide ones.
func (a *Agent) ForkSession(sessionID string, opts ...SessionOption) (*Session, error) {
	cfg := applySessionOptions(opts)
	// Look up parent — prefer the live in-memory session (holds the mutex);
	// fall back to opening from disk for inactive sessions.
	a.sessMu.Lock()
	parent, active := a.activeSessions[sessionID]
	a.sessMu.Unlock()

	var parentStore *store.Session
	if active {
		parentStore = parent.store
	} else {
		s, err := store.OpenSession(a.store, sessionID)
		if err != nil {
			return nil, fmt.Errorf("fork: open parent: %w", err)
		}
		if s == nil {
			return nil, fmt.Errorf("fork: session %s not found", sessionID)
		}
		parentStore = s
	}

	forkStore, err := parentStore.Fork(defaultSessionName(time.Now()))
	if err != nil {
		return nil, err
	}

	return a.openStoredCopy(forkStore, cfg, "fork")
}

// openStoredCopy opens a live session over a session store that was just
// written as a COPY (a fork, an import): the model (with ResumeSession's
// fallback), its tools, prompt and channels, registered on the agent. On any
// failure the copy is deleted, so nothing half-made is left. what prefixes the
// errors ("fork", "import").
func (a *Agent) openStoredCopy(copyStore *store.Session, cfg sessionConfig, what string) (*Session, error) {
	meta := copyStore.Meta()
	// Same rule as ResumeSession: an unavailable provider falls back to the default
	// model rather than making the (copied) history unreachable.
	provider, modelID, fallback, err := resolveSessionModel(meta.Model)
	if err != nil {
		_ = a.store.DeleteSession(meta.ID)
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if fallback != nil {
		meta.Model = fallback.To
		if err := copyStore.UpdateMeta(meta); err != nil {
			_ = a.store.DeleteSession(meta.ID)
			return nil, fmt.Errorf("%s: persist model fallback: %w", what, err)
		}
	}

	// Profiles: the copy keeps the names it came with (a fork: the parent's; an
	// import: the export's); SessionWithEnabledProfiles replaces them. A stored name
	// that is not registered is skipped and reported, an explicit one fails.
	cfgOwn := cfg.own
	profNames, cfg, missingProfiles, err := a.profilesFor(meta.Profiles, cfg)
	if err != nil {
		_ = copyStore.Close()
		_ = a.store.DeleteSession(meta.ID)
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if cfg.setProfile {
		meta.Profiles = profNames
		if err := copyStore.UpdateMeta(meta); err != nil {
			_ = copyStore.Close()
			_ = a.store.DeleteSession(meta.ID)
			return nil, fmt.Errorf("%s: persist profiles: %w", what, err)
		}
	}

	thinkingLvl := meta.Thinking

	// Same restore-over-default logic as ResumeSession: Fork already copies
	// MaxIterations onto the fork's own persisted meta (see store.Session.Fork),
	// so this just decides what the LIVE session uses.
	maxIterations := a.maxIterations
	if meta.MaxIterations > 0 {
		maxIterations = meta.MaxIterations
	}

	maxTokens := sessionMaxTokens(a.maxTokens, provider.ModelMeta(modelID))

	cwd := meta.CWD
	res, _ := a.loadResources(cwd)
	skills, readSkill := res.Skills, skillReader(res.Skills)

	var sess *Session
	// A fork listens to nothing of its parent's; it may be given channels of
	// its own (opts), exactly like a new session.
	chans, chDirectives, chTools, err := a.prepareChannels(meta.ID, cwd, a.withSessionChannels(cfg.channels), cfg.tools)
	if err != nil {
		_ = copyStore.Close()
		_ = a.store.DeleteSession(meta.ID)
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	forkTools, tl := a.buildSessionTools(meta.ID, cwd, &sess, res, append(append([]tools.Tool(nil), cfg.tools...), chTools...)...)
	forkPrompt, pl := a.buildSystemPrompt(cwd, res, append(append([]string(nil), cfg.directives...), chDirectives...)...)
	sess = newSession(copyStore,
		provider, modelID, thinkingLvl,
		forkTools, tl, forkPrompt, pl,
		maxIterations, maxTokens,
		skills, readSkill,
		a.memStore != nil)
	sess.agent = a
	sess.ownDirectives = append([]string(nil), cfgOwn...)
	sess.modelFallback = fallback
	sess.missingProfiles.Store(missingProfiles)
	if err := sess.attachChannels(chans); err != nil {
		_ = copyStore.Close()
		_ = a.store.DeleteSession(meta.ID)
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	a.registerSession(sess)
	return sess, nil
}

// ── Internal helpers ─────────────────────────────────────────────────────

// toolLens holds the total raw byte size of all tool schema JSON sent to the
// model. Stored as bytes so ContextBreakdown() can apply the correct
// chars-per-token divisor for the active provider family at query time.
type toolLens struct {
	totalBytes int // JSON byte length of all tool schemas (built-in + MCP)
}

// sessRef is a pointer-to-pointer the caller fills in with the real *Session
// once it exists (buildSessionTools runs BEFORE newSession, so there is no
// session to reference yet at construction time — see the call sites in
// NewSession/ResumeSession/ForkSession, each of which does
// `sess = newSession(...)` into the same variable this points at
// immediately after building it). The Subagent tool's executor closure
// captures sessRef (not a model string) specifically so it can read
// (*sessRef).Model() at EXECUTION time — reflecting any SwitchModel
// call made since the session was created — instead of freezing whatever
// model the session happened to have when its tools were first built. That
// freeze was a real bug: a session created with a rate-limited model, then
// switched via /model (or ACP's session/set_config_option) to a working
// one, kept spawning sub-agents against the ORIGINAL model — the main
// session itself worked fine (it reads its own live s.modelID every turn),
// only Subagent was stuck, because its closure had captured a plain string
// once and never looked at it again.
// awaitSubagentResult waits for a sub-agent turn to finish and returns its
// accumulated text plus the error the Subagent tool should act on.
//
// done is fed by the executor's event subscription: nil on EventTurnEnd, a
// non-nil error on EventError. The subtlety it guards is a race on TIMEOUT:
// when the sub-agent's ctx deadline expires, the session cancels its own turn
// and (via a defer) still emits EventTurnEnd — which this subscription maps to
// done<-nil — at essentially the same moment ctx.Done() closes. A naive select
// would then pick the <-done branch at random and return a NIL error, so the
// Subagent tool's isTimeout() would never fire and the deadline would be
// silently reported as a successful partial result.
//
// The guard: a TurnEnd(nil) that arrives with ctx already cancelled is a
// timeout in disguise, not a success — so whenever ctx.Err() is set, it wins,
// carrying the context.DeadlineExceeded (timeout) or context.Canceled (user
// Stop) that the caller needs to classify the outcome correctly. A genuine
// success (TurnEnd with ctx still live) returns nil as before.
func awaitSubagentResult(ctx context.Context, done <-chan error, textBuf *strings.Builder) (string, error) {
	text := func() string { return strings.TrimSpace(textBuf.String()) }
	select {
	case err := <-done:
		if err == nil && ctx.Err() != nil {
			return text(), ctx.Err()
		}
		return text(), err
	case <-ctx.Done():
		return text(), ctx.Err()
	}
}

// buildFetchSummarizer returns the FetchSummarizer closure the Fetch tool
// uses to condense fetched content when the model sets 'prompt' — see
// fetch.go's FetchSummarizer doc comment for why this exists at all (Fetch
// gained this field because the model, seeing the tool renamed to "WebFetch"
// under claude-oauth, kept sending a 'prompt' argument our schema never
// declared or used, matching Anthropic's real WebFetch tool's behavior).
//
// This is deliberately the SAME mechanism as the Subagent tool's executor
// (see buildSessionTools below) — an ephemeral, in-process sub-agent reading
// the CURRENT session model at call time via sessRef — but several tiers
// lighter: fetchSummarizeSystemPrompt (a narrower, single-purpose prompt
// than subagentSystemPrompt), fetchSummarizeMaxIterations (5, not 50 — a
// condensing job is a single-turn read-and-answer, never a multi-step task),
// EVERY built-in tool disallowed (unlike Subagent's sub-agent, which keeps
// most tools and only blocks recursion/write-memory/schedule-management),
// and no sharedMemory at all — there is nothing to recall or persist for a
// one-shot condensing call. Its only input is the content Fetch already
// downloaded, handed to it as part of the prompt text, never re-fetched.
func (a *Agent) buildFetchSummarizer(cwd string, sessRef **Session) tools.FetchSummarizer {
	parentA := a
	return func(ctx context.Context, prompt, content string) (string, error) {
		subAgent := New(AgentOptions{
			ThinkingLevel:  parentA.thinkingLevel,
			SystemPrompt:   fetchSummarizeSystemPrompt,
			MaxIterations:  fetchSummarizeMaxIterations,
			MaxTokens:      parentA.maxTokens,
			Store:          store.NewInMemoryStore(),
			ResourceLoader: parentA.resourceLoader, // stateless: shared, never copied
			// TOOLLESS on purpose: this sub-agent's only input is the content
			// Fetch already downloaded, handed to it as prompt text — it has
			// nothing to read, write, run, or fetch, and no memory to recall
			// (no sharedMemory below, so a.memStore stays nil and Memo* never
			// registers regardless of this list). Every built-in name is
			// listed explicitly rather than relying on the absence of
			// MCPServers/Memory/opts.Tools alone —
			// defense in depth so a future default change elsewhere can't
			// silently hand this single-purpose summarizer a tool it was
			// never meant to have.
			DisallowedTools: []string{
				tools.ToolBash, tools.ToolRead, tools.ToolWrite, tools.ToolEdit, tools.ToolFetch,
				tools.ToolSkill, tools.ToolSubagent,
				tools.ToolMemoWrite, tools.ToolMemoSearch, tools.ToolMemoDelete,
				tools.ToolSchedule, tools.ToolScheduleList, tools.ToolScheduleDelete,
			},
		})
		// Current model at call time, not whatever was active when this
		// closure was built — same reasoning as Subagent's executor (see its
		// comment on sessRef): a /model switch mid-session must be reflected
		// in every subsequent Fetch condensing call too.
		currentModel := (*sessRef).Model()
		sess, err := subAgent.NewSession(cwd, currentModel)
		if err != nil {
			return "", fmt.Errorf("fetch summarizer: %w", err)
		}
		// Fold this ephemeral sub-agent's token/cost spend into the PARENT
		// session's own totals before it's discarded — see
		// Session.addDelegatedCost's doc comment for the full reasoning
		// (billing visibility without ever touching the parent's
		// ContextUsage/context window). syncStats() (not the lock-free
		// Stats()) so any of THIS ephemeral session's own delegated cost —
		// e.g. a nested sub-agent it launched — is drained and folded in
		// before being read; Stats() alone could still be missing it. Runs
		// before Close() so the drained total is captured while still
		// populated; order in this defer chain doesn't matter for
		// correctness (addDelegatedCost is lock-free), only that both
		// happen.
		defer func() {
			if parent := *sessRef; parent != nil {
				parent.addDelegatedCost(sess.syncStats())
			}
			sess.Close()
		}()

		fullPrompt := fmt.Sprintf("Instruction: %s\n\n--- Content ---\n%s", prompt, content)

		var textBuf strings.Builder
		done := make(chan error, 1)
		sess.Subscribe(func(e types.Event) {
			switch e.Type {
			case types.EventStreamTextDelta:
				textBuf.WriteString(e.Delta)
			case types.EventTurnEnd:
				done <- nil
			case types.EventError:
				done <- fmt.Errorf("%s", e.Message)
			}
		})
		sess.Prompt(ctx, fullPrompt)
		return awaitSubagentResult(ctx, done, &textBuf)
	}
}

// buildSessionTools assembles one session's tool registry. sessionTools
// (SessionWithTools) are registered right after the Agent-wide registry, under
// the same DisallowedTools filter.
func (a *Agent) buildSessionTools(sessionID, cwd string, sessRef **Session, res *resources.Resources, sessionTools ...tools.Tool) (*tools.Registry, toolLens) {
	reg := tools.NewRegistry()
	// Built one instance per session, bound to THIS session's cwd — they can't
	// live in the shared agent-level registry (a.toolReg, seeded once in New()
	// before any session or its cwd exists) because each session can have a
	// DIFFERENT cwd. A relative path/command resolves against this cwd
	// (resolvePath for Read/Write/Edit, cmd.Dir for Bash), not the hosting OS
	// process's real working directory.
	if a.isToolAllowed(tools.ToolBash) {
		reg.Register(tools.Bash(cwd))
	}
	if a.isToolAllowed(tools.ToolRead) {
		reg.Register(tools.ReadFile(cwd))
	}
	if a.isToolAllowed(tools.ToolWrite) {
		reg.Register(tools.WriteFile(cwd))
	}
	if a.isToolAllowed(tools.ToolEdit) {
		reg.Register(tools.Edit(cwd))
	}
	if a.isToolAllowed(tools.ToolFetch) {
		reg.Register(tools.Fetch(a.buildFetchSummarizer(cwd, sessRef)))
	}
	for _, def := range a.toolReg.Definitions() {
		if a.isToolAllowed(def.Name) {
			reg.Register(a.toolReg.Get(def.Name))
		}
	}
	for _, t := range sessionTools {
		if a.isToolAllowed(t.Def.Name) {
			reg.Register(t)
		}
	}
	if len(res.Skills) > 0 {
		reg.Register(tools.Skill(skillReader(res.Skills)))
	}
	// Memory tools — project-scoped persistent memory, registered when a store is
	// configured. cwd partitions memories per project (like sessions). The store
	// is wrapped in a scoped adapter that hides cwd from the agent — the agent
	// only ever operates within its session's cwd.
	if a.memStore != nil {
		memAdapter := memory.NewToolAdapter(a.memStore)
		if a.isToolAllowed(tools.ToolMemoWrite) {
			reg.Register(tools.MemoWrite(memAdapter, cwd))
		}
		if a.isToolAllowed(tools.ToolMemoSearch) {
			reg.Register(tools.MemoSearch(memAdapter, cwd))
		}
		if a.isToolAllowed(tools.ToolMemoDelete) {
			reg.Register(tools.MemoDelete(memAdapter, cwd))
		}
	}
	// Schedule tools — manage cron-scheduled prompts. Registered when scheduling
	// is enabled; the agent owns the store and the engine that fires them.
	if adapter := a.scheduleAdapter(); adapter != nil {
		if a.isToolAllowed(tools.ToolSchedule) {
			// owner = this session's id: the engine routes a fired prompt back here.
			reg.Register(tools.Schedule(adapter, sessionID, func() string {
				if *sessRef == nil {
					return ""
				}
				return (*sessRef).scheduleOrigin()
			}))
		}
		if a.isToolAllowed(tools.ToolScheduleList) {
			// owner = this session: it only sees its own schedules.
			reg.Register(tools.ScheduleList(adapter, sessionID))
		}
		if a.isToolAllowed(tools.ToolScheduleDelete) {
			reg.Register(tools.ScheduleDelete(adapter, sessionID))
		}
	}

	// WebSearch — built-in MiniMax-backed search tool. The tool owns its HTTP
	// client and reaches for the active minimax provider's API key at call
	// time. We pass a thin ProviderLookup facade instead of letting the tool
	// import internal/providers; the agent is the single place that knows
	// both sides. Off unless EnableWebSearch; gated by the same allow-list
	// as every other built-in.
	if a.opts.EnableWebSearch && a.isToolAllowed(tools.ToolWebSearch) {
		// nil client → tool uses its own 30s default; per-call timeouts
		// come from the tool's `timeout` param, not from the shared client.
		reg.Register(tools.WebSearch(a.webSearchLookup(), nil))
	}

	// SessionInfo — a simple, purely informational snapshot of this
	// session's own identity/config/environment/usage. Always registered
	// (like Bash/Read/Write/Edit/Fetch) — no separate AgentOptions.EnableX
	// flag; DisallowedTools is the only way to turn it off. It used to
	// share a single EnableSessionInfo flag with SessionSearch (full-text
	// search over the session's own history), removed for being largely
	// unused in practice and redundant with persistent memory
	// (MemoWrite/MemoSearch) — see docs/plans/2026-09-22-sessionsearch-removal.md.
	//
	// The closure reads from *sessRef at EXECUTION time (same pattern
	// buildFetchSummarizer/Subagent's executor already use for Model()) so
	// it always reflects live state, not whatever was true when
	// buildSessionTools ran.
	if a.isToolAllowed(tools.ToolSessionInfo) {
		// Deliberately built from the lock-free public getters (ID/CWD/
		// Name/Model/Thinking/CreatedAt/Stats — every one of these is safe
		// by construction: none of them take s.mu), NEVER the unexported
		// syncMeta()/syncStats() (which DO take s.mu to drain+persist
		// pending delegated cost). This closure runs INSIDE a tool executor
		// while promptSync holds s.mu for the whole turn — calling
		// syncMeta()/syncStats() here would deadlock instantly with no
		// timeout, no error — exactly the class of bug Model() was
		// introduced to fix (see its own doc comment and the
		// subagent-timeout-background project memory this mirrors).
		//
		// MCPStatuses()/Schedules() below take THEIR OWN locks
		// (mcp.Manager.mu / schedule.Store.mu respectively) — never s.mu —
		// so calling them from inside this closure carries no deadlock
		// risk at all, same reasoning server.go's handleSessionInfo
		// already relies on for the identical data.
		reg.Register(tools.SessionInfo(func() tools.SessionInfoSnapshot {
			sess := *sessRef
			stats := sess.Stats()

			mcpConnected := 0
			for _, st := range a.MCPStatuses() {
				if st.Connected {
					mcpConnected++
				}
			}
			scheduleCount := 0
			if sc := a.schedules(); sc != nil {
				for _, s := range sc.List() {
					if s.Owner == sessionID {
						scheduleCount++
					}
				}
			}

			return tools.SessionInfoSnapshot{
				ID:        sess.ID(),
				CWD:       sess.CWD(),
				Name:      sess.Name(),
				Model:     sess.Model(),
				Thinking:  sess.Thinking(),
				CreatedAt: sess.CreatedAt().Format(time.RFC3339),

				Version:       version.Version,
				MCPConnected:  mcpConnected,
				ScheduleCount: scheduleCount,
				OS:            runtime.GOOS,
				Arch:          runtime.GOARCH,
				Terminal:      a.opts.Terminal,
				Channels:      sessionChannelInfos(sess.Channels()),

				InputTokens:   stats.InputTokens,
				OutputTokens:  stats.OutputTokens,
				CacheRead:     stats.CacheRead,
				CacheWrite:    stats.CacheWrite,
				CostUSD:       stats.CostUSD,
				ContextUsage:  stats.ContextUsage,
				ContextWindow: stats.ContextWindow,
			}
		}))
	}

	// Subagent tool — only if allowed (excluded for sub-agents themselves)
	if a.isToolAllowed(tools.ToolSubagent) {
		executor := func(ctx context.Context, prompt string, requestedMaxIterations int, readonly bool) (string, error) {
			// (*sessRef) is non-nil by the time a tool can run: the model
			// cannot call it before the session's first Prompt.
			return (*sessRef).launchSubagent(prompt, requestedMaxIterations, readonly)
		}
		reg.Register(tools.Subagent(executor))
	}

	// Measure the total raw byte size of all tool schema JSON. Stored as bytes
	// so ContextBreakdown() can apply the correct chars-per-token divisor for
	// the active provider at query time (Anthropic=6, OpenAI=4).
	var tl toolLens
	for _, def := range reg.Definitions() {
		b, _ := json.Marshal(def)
		tl.totalBytes += len(b)
	}
	return reg, tl
}

// isToolAllowed reports whether a tool may be used. A tool is allowed unless it
// appears in the DisallowedTools blocklist (empty blocklist = everything
// allowed). Using a blocklist means MCP tools (mcp__*) pass through by default.
func (a *Agent) isToolAllowed(name string) bool {
	for _, n := range a.disallowedTools {
		if n == name {
			return false
		}
	}
	return true
}

// promptLens holds the total byte length of the system prompt. Computed once
// inside buildSystemPrompt so ContextBreakdown() can estimate token counts
// without recomputing anything.
type promptLens struct {
	total int // full system prompt byte length
}

// buildSystemPrompt assembles a session's system prompt. sessionDirectives
// (SessionWithDirectives) go last, after the Agent-wide directives.
func (a *Agent) buildSystemPrompt(cwd string, res *resources.Resources, sessionDirectives ...string) (string, promptLens) {
	var b strings.Builder

	// Precedence: the prompt the caller set > SYSTEM.md > the built-in default.
	switch {
	case a.systemPrompt != "":
		b.WriteString(a.systemPrompt)
	case res.SystemMD != "":
		b.WriteString(res.SystemMD)
	default:
		b.WriteString(defaultSystemPrompt)
	}

	if len(res.Skills) > 0 {
		b.WriteString("\n\n## Available Skills\n\nSkills are specialized guides for specific tasks. When a task matches a skill, load it with the Skill tool before starting — it contains workflows and constraints you must follow.\n\n")
		for _, s := range res.Skills {
			b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
		}
	}

	if a.memStore != nil {
		b.WriteString("\n\n## Memory\n\nYou have persistent, project-scoped memory that carries over between sessions. At the start of a task — or whenever you lack context about earlier work — use MemoSearch with relevant keywords to recover prior decisions, conventions, and context. Save durable, high-value insights with MemoWrite (never transient task state), and remove obsolete ones with MemoDelete.")
		// List up to 30 memory slugs so the model knows what it has and can
		// proactively search. Fetch 31: if we get 31, there are more than 30
		// and we note "+ many more" without flooding the prompt.
		if res, err := a.memStore.Search(cwd, "", false, 0, 31); err == nil {
			if len(res.Results) == 0 {
				b.WriteString("\n\nNo memories yet — use MemoWrite to save durable insights as you work.")
			} else {
				b.WriteString("\n\nYour memories:\n")
				count := len(res.Results)
				if count > 30 {
					count = 30
				}
				for i := 0; i < count; i++ {
					b.WriteString(fmt.Sprintf("- %s\n", res.Results[i].Slug))
				}
				if len(res.Results) > 30 {
					b.WriteString("- many more — use MemoSearch to find them\n")
				}
			}
		}
	}

	// Deliberately brief — just a pointer that this capability exists.
	// SessionInfo's own Description carries the actual detail (what it
	// returns, when to use it); duplicating that here would be redundant.
	// Gated on isToolAllowed (not a dedicated EnableX flag — SessionInfo is
	// always registered like any other built-in; DisallowedTools is the
	// only way to turn it off, see buildSessionTools' own comment).
	if a.isToolAllowed(tools.ToolSessionInfo) {
		b.WriteString("\n\n## Session Tools\n\nYou have SessionInfo (this session's own identity/config) available — use it when you need information about the current session itself.")
	}

	// Gated on EnableScheduler, matching scheduleAdapter()'s exact
	// condition — not a.schedStore != nil (the store always opens
	// regardless, but the tools this section describes are only actually
	// registered when this instance runs the engine; mentioning them
	// otherwise would describe tools the model doesn't have).
	if a.opts.EnableScheduler {
		b.WriteString("\n\n## Scheduling\n\nYou can schedule prompts to run automatically on a recurring cron schedule. Use Schedule to create or update one, ScheduleList to review what's scheduled and how often it has run, and ScheduleDelete to remove one. Schedule work the user wants done repeatedly on a cadence; the prompt runs later exactly as if the user sent it.")
	}

	b.WriteString(fmt.Sprintf("\n\n## Working Directory\n\n%s\n\nPrefer absolute paths when reading, writing, or editing files, and when running Bash commands that touch specific files. Relative paths resolve against this working directory, but an absolute path is unambiguous.\n", cwd))

	if res.AgentsMD != "" {
		b.WriteString("\n\n## Project Context\n\n")
		b.WriteString(res.AgentsMD)
	}

	// Caller-supplied directives (e.g. a frontend's capabilities). Appended last
	// so they can reference everything above.
	// Session-scoped directives (SessionWithDirectives) after them. A block
	// identical to one already appended is skipped, so a caller that still
	// passes the same directive at both levels doesn't get it twice.
	seen := map[string]bool{}
	for _, d := range append(append([]string(nil), a.opts.Directives...), sessionDirectives...) {
		if d = strings.TrimSpace(d); d != "" && !seen[d] {
			seen[d] = true
			b.WriteString("\n\n")
			b.WriteString(d)
		}
	}

	full := b.String()
	return full, promptLens{total: len(full)}
}

// defaultSessionName generates the initial session name — date + time.
// Replaced automatically by the first user message after Prompt() is called.
func defaultSessionName(t time.Time) string {
	return "New Session " + t.Format("2006-01-02 15:04")
}

// isDefaultSessionName returns true if the name matches the auto-generated date format.

// sessionChannelInfos converts the session's channels for SessionInfo (always
// a non-nil slice, so the tool reports [] rather than null).
func sessionChannelInfos(chs []ChannelInfo) []tools.SessionChannelInfo {
	out := make([]tools.SessionChannelInfo, 0, len(chs))
	for _, c := range chs {
		out = append(out, tools.SessionChannelInfo{Type: c.Type, ID: c.ID, Name: c.Name})
	}
	return out
}

// withSessionChannels is the channels a session opens with: every registered
// AutoChannel (it joins each session by itself) first, then the caller's.
func (a *Agent) withSessionChannels(reqs []types.InputChannel) []types.InputChannel {
	var auto []types.InputChannel
	for _, ch := range a.opts.Channels {
		if ac, ok := ch.(AutoChannel); ok && ac != nil {
			auto = append(auto, types.InputChannel{Type: ch.Type(), Ref: ac.AutoRef()})
		}
	}
	if len(auto) == 0 {
		return reqs
	}
	return append(auto, reqs...)
}
