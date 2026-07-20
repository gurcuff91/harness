package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/schedule"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/mcp"
	"github.com/gurcuff91/harness/types"
)

// ── Agent ────────────────────────────────────────────────────────────────

// Agent is a pure factory — it holds global config and spawns Sessions.
// It has zero knowledge of providers, credentials, or which providers are active.
// The caller is responsible for ensuring the provider is active before NewSession().
type Agent struct {
	opts            AgentOptions // original opts — used by Subagent tool to clone
	toolReg         *tools.Registry
	disallowedTools []string
	store           store.SessionStore
	resourceLoader  resources.ResourceLoader // nil = FileResourceLoader(cwd) per session
	thinkingLevel   string
	systemPrompt    string
	maxTurns        int
	maxTokens       int          // 0 = resolved from ModelMeta in NewSession
	mcpManager      *mcp.Manager // non-nil only when EnableMCPs; owns MCP subprocesses

	// Memory (non-nil only when enabled). ownsMemory is true when this agent
	// opened the store itself (root agent) and must Close it; false when it shares
	// a parent's store (subagent), which must not be closed here.
	memStore   *memory.Store
	ownsMemory bool

	// Scheduling (non-nil only when EnableScheduler). The agent owns the store
	// (for the Schedule* tools) and the engine (which fires due prompts).
	schedStore  *schedule.Store
	schedEngine *schedule.Engine

	// activeSessions tracks every live session (created via NewSession or
	// ResumeSession, removed on Close) by id. The scheduler routes a fired prompt
	// to the session named by the schedule's owner; a transport reaches whatever
	// output it wants by subscribing to that session's events. Guarded by sessMu.
	sessMu         sync.Mutex
	activeSessions map[string]*Session
}

// AgentOptions configures a new Agent.
type AgentOptions struct {
	// ── Thinking ─────────────────────────────────────────────────────────
	ThinkingLevel string // "disable"|"low"|"medium"|"high"|"xhigh"

	// ── Behavior ─────────────────────────────────────────────────────────
	SystemPrompt string   // base system prompt for all sessions
	Directives   []string // extra instruction blocks appended to the system prompt (e.g. transport-specific capabilities)
	MaxTurns     int      // max ReAct iterations per turn — default: 25
	MaxTokens    int      // max output tokens — default: model's MaxTokens from ModelMeta

	// ── Tools ────────────────────────────────────────────────────────────
	Tools           []tools.Tool // additional tools (defaults always included)
	DisallowedTools []string     // tool names to exclude — empty = all allowed
	EnableMCPs      bool         // spawn & connect configured MCP servers (root agent only)

	// ── Infrastructure (optional) ────────────────────────────────────────
	Store          store.SessionStore // default: in-memory
	ResourceLoader resources.ResourceLoader  // default: FileResourceLoader(cwd) per session
	//                                         // pass NilLoader{} to disable discovery

	// EnableMemory turns on project-scoped persistent memory: the agent opens the
	// shared memory store (~/.harness/agent/memory.db) and registers the Memo*
	// tools. Off by default.
	EnableMemory bool
	// sharedMemory lets a subagent reuse its parent's already-open store instead
	// of opening its own. Unexported: only agent.go sets it (subagent path); SDK
	// callers use EnableMemory.
	sharedMemory *memory.Store
	// EnableScheduler turns on cron-scheduled prompts: the Schedule* management
	// tools AND the engine that fires due prompts. The agent owns both. A
	// transport marks one session as the scheduler target (SetScheduledSession);
	// only one agent should enable this so prompts don't fire twice.
	EnableScheduler bool
}

// New creates a new Agent. Never fails — provider is resolved per session.
func New(opts AgentOptions) *Agent {
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = 25
	}
	if opts.SystemPrompt == "" {
		opts.SystemPrompt = defaultSystemPrompt
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

	reg := defaultTools()

	// Connect configured MCP servers eagerly (root agent only). Their tools are
	// registered alongside the built-ins and shared by every session. Failures
	// degrade silently — recorded in the manager's Statuses(), never logged to
	// stdout (which would corrupt the TUI).
	var mcpMgr *mcp.Manager
	if opts.EnableMCPs {
		mcpMgr = mcp.NewManager()
		for _, t := range mcpMgr.Start(context.Background()) {
			reg.Register(t)
		}
	}

	// Additional tools (built-ins always included). Subagents receive the
	// parent's MCP tools here without spawning their own processes.
	for _, t := range opts.Tools {
		reg.Register(t)
	}

	a := &Agent{
		opts:            opts,
		toolReg:         reg,
		disallowedTools: opts.DisallowedTools,
		store:           opts.Store,
		resourceLoader:  opts.ResourceLoader,
		thinkingLevel:   opts.ThinkingLevel,
		systemPrompt:    opts.SystemPrompt,
		maxTurns:        opts.MaxTurns,
		maxTokens:       opts.MaxTokens,
		mcpManager:      mcpMgr,
		activeSessions:  make(map[string]*Session),
	}

	// Scheduling: the agent always opens the store so the Schedule* management
	// tools work in any session. EnableScheduler only decides whether this agent
	// also RUNS the engine that fires due prompts — so a plain session can manage
	// schedules while exactly one agent (the one with --scheduler) executes them.
	// Subagents get neither: they pass EnableScheduler=false and disallow the
	// Schedule* tools.
	if st, err := schedule.Open(""); err == nil {
		a.schedStore = st
		if opts.EnableScheduler {
			a.schedEngine = schedule.NewEngine(st, a.fireScheduledPrompt)
			a.schedEngine.Start(context.Background())
		}
	}

	// Memory: a subagent shares its parent's already-open store (sharedMemory);
	// a root agent with EnableMemory opens its own (and owns closing it). Failure
	// to open degrades silently — memory tools simply stay unregistered.
	if opts.sharedMemory != nil {
		a.memStore = opts.sharedMemory
	} else if opts.EnableMemory {
		if m, err := memory.Open(""); err == nil {
			a.memStore = m
			a.ownsMemory = true
		}
	}

	return a
}

// fireScheduledPrompt is the engine callback: it routes the due prompt to the
// session named by the schedule's owner, tagged as scheduled. If that session is
// not currently active (never opened, or closed), the prompt is dropped — the
// engine still records the run, so no catch-up piles up. A transport that has
// subscribed to the owner session's events will see the output; nobody being
// subscribed doesn't stop the prompt from running.
//
// owner == "" is the single-session fallback (e.g. the TUI): if exactly one
// session is active, it receives the prompt.
func (a *Agent) fireScheduledPrompt(slug, prompt, owner string) {
	if sess := a.resolveScheduledSession(owner); sess != nil {
		sess.Prompt(context.Background(), prompt, WithOriginScheduled())
	}
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

// registerSession adds a live session to the active set (keyed by id). Called by
// NewSession/ResumeSession. unregisterSession removes it (called on Close).
func (a *Agent) registerSession(s *Session) {
	a.sessMu.Lock()
	a.activeSessions[s.id] = s
	a.sessMu.Unlock()
}

func (a *Agent) unregisterSession(id string) {
	a.sessMu.Lock()
	delete(a.activeSessions, id)
	a.sessMu.Unlock()
}

// scheduleAdapter exposes the agent's schedule store to the Schedule* tools.
// Returns nil when scheduling is disabled.
func (a *Agent) scheduleAdapter() tools.ScheduleStore {
	if a.schedStore == nil {
		return nil
	}
	return schedule.NewToolAdapter(a.schedStore)
}

// Schedules returns the agent's schedule store (nil if unavailable). Read by the
// HTTP transport to serve the read-only /api/schedules listing.
func (a *Agent) Schedules() *schedule.Store { return a.schedStore }

// Options returns the original configuration — used by the Subagent tool to clone.
func (a *Agent) Options() AgentOptions {
	return a.opts
}

// MCPTools returns the agent's MCP tools, for sharing with subagents (which set
// EnableMCPs=false and receive these via AgentOptions.Tools, reusing the
// parent's live MCP processes). Nil when MCP is disabled.
func (a *Agent) MCPTools() []tools.Tool {
	if a.mcpManager == nil {
		return nil
	}
	return a.mcpManager.Tools()
}

// Memory exposes the agent's persistent memory store (nil if memory is
// disabled). This is the rich, cwd-aware store — used by the HTTP transport to
// serve read-only memory queries, and available to SDK consumers. The agent's
// own tools use a scoped adapter over the same store.
func (a *Agent) Memory() *memory.Store { return a.memStore }

// Providers returns a read-only snapshot of every known provider and its state.
// This is the SDK's window into provider configuration; administration
// (connecting/disconnecting, entering API keys, OAuth) is done via the `harness`
// CLI — which is why no credentials are exposed here. Active providers lazily
// fetch their model list on first call.
func (a *Agent) Providers() []types.ProviderInfo {
	providers.EnsureRegistry()
	var out []types.ProviderInfo
	for _, p := range providers.All {
		models := p.Models()
		if p.IsActive() && len(models) == 0 {
			models, _ = p.FetchModels()
		}
		out = append(out, types.ProviderInfo{
			Name:           p.Name(),
			DisplayName:    p.DisplayName(),
			Description:    p.Description(),
			Active:         p.IsActive(),
			CredentialType: p.CredentialType(),
			ModelCount:     len(models),
		})
	}
	return out
}

// Models returns every available model across all ACTIVE providers, each tagged
// with its provider and a fully-qualified "provider/model" id ready to pass to
// NewSession. Inactive providers are skipped. Models are lazily fetched.
func (a *Agent) Models() []types.ModelListing {
	providers.EnsureRegistry()
	var out []types.ModelListing
	for _, p := range providers.All {
		if !p.IsActive() {
			continue
		}
		models := p.Models()
		if len(models) == 0 {
			models, _ = p.FetchModels()
		}
		for _, m := range models {
			out = append(out, types.ModelListing{
				Provider:  p.Name(),
				Model:     p.Name() + "/" + m.ID,
				ModelMeta: m,
			})
		}
	}
	return out
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
// closes the memory database. Only the root agent should be closed — subagents
// are ephemeral, have no MCP manager, and merely share the parent's memory
// store (which they must not close). Idempotent and nil-safe; both resources are
// released even if one fails.
func (a *Agent) Close() error {
	var errs []error
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
	return errors.Join(errs...)
}

// NewSession creates a fresh session for the given working directory and model.
// model is required in "provider/model" format (e.g. "anthropic/claude-sonnet-4").
// Returns error if the provider is not active or the model doesn't exist.
func (a *Agent) NewSession(cwd, model string) (*Session, error) {
	// Resolve provider — validates active + model exists
	provider, modelID, err := providers.Resolve(model)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}

	// MaxTokens from model if not set
	maxTokens := a.maxTokens
	if maxTokens == 0 {
		if meta := provider.ModelMeta(modelID); meta != nil && meta.MaxTokens > 0 {
			maxTokens = meta.MaxTokens
		} else {
			maxTokens = 32000
		}
	}

	// Resources
	loader := a.resourceLoader
	if loader == nil {
		loader = resources.NewFileResourceLoader(cwd)
	}
	res, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("load resources: %w", err)
	}

	now := time.Now()
	thinking := a.thinkingLevel
	if thinking == "" {
		thinking = "off"
	}
	// The id is generated first so the session's Schedule tool can capture it as
	// the owner for any schedules it creates.
	sessionID := uuid.New().String()
	sessionTools := a.buildSessionTools(sessionID, cwd, model, res, loader)
	systemPrompt := a.buildSystemPrompt(cwd, res)

	meta := store.SessionMeta{
		ID:           sessionID,
		CWD:          cwd,
		Name:         defaultSessionName(now),
		Model:        model,
		Thinking:     thinking,
		CreatedAt:    now,
		LastActiveAt: now,
	}
	storeInst, err := store.CreateSession(a.store, meta)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	sess := newSession(storeInst,
		provider, modelID, a.thinkingLevel,
		sessionTools, systemPrompt,
		a.maxTurns, maxTokens,
		res.Skills, loader.ReadSkill)
	sess.agent = a
	a.registerSession(sess)
	return sess, nil
}

// ResumeSession reopens an existing session, fully restoring its state.
func (a *Agent) ResumeSession(sessionID string) (*Session, error) {
	storeInst, err := store.OpenSession(a.store, sessionID)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if storeInst == nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	meta := storeInst.Meta()

	// Restore provider+model — error if provider no longer available
	provider, modelID, err := providers.Resolve(meta.Model)
	if err != nil {
		return nil, fmt.Errorf("resume session: provider %q no longer available: %w", meta.Model, err)
	}

	thinkingLvl := a.thinkingLevel
	if meta.Thinking != "" {
		thinkingLvl = meta.Thinking
	}

	maxTokens := a.maxTokens
	if maxTokens == 0 {
		if m := provider.ModelMeta(modelID); m != nil && m.MaxTokens > 0 {
			maxTokens = m.MaxTokens
		} else {
			maxTokens = 32000
		}
	}

	cwd := meta.CWD
	loader := a.resourceLoader
	if loader == nil {
		loader = resources.NewFileResourceLoader(cwd)
	}
	res, _ := loader.Load()
	var skills []resources.SkillInfo
	var readSkill func(string) (content string, dir string, err error)
	if res != nil {
		skills = res.Skills
		readSkill = loader.ReadSkill
	}

	sess := newSession(storeInst,
		provider, modelID, thinkingLvl,
		a.buildSessionTools(meta.ID, cwd, meta.Model, res, loader),
		a.buildSystemPrompt(cwd, res),
		a.maxTurns, maxTokens,
		skills, readSkill)
	sess.agent = a
	a.registerSession(sess)
	return sess, nil
}

// ── Session management ───────────────────────────────────────────────────

func (a *Agent) ListSessions(cwd string) ([]store.SessionMeta, error) {
	return a.store.ListMetas(cwd)
}

func (a *Agent) ListAllSessions() ([]store.SessionMeta, error) {
	return a.store.ListMetas("")
}

func (a *Agent) DeleteSession(sessionID string) error {
	return a.store.DeleteSession(sessionID)
}

func (a *Agent) RenameSession(sessionID, name string) error {
	return store.Rename(a.store, sessionID, name)
}

// ── Internal helpers ─────────────────────────────────────────────────────

func (a *Agent) buildSessionTools(sessionID, cwd, model string, res *resources.Resources, loader resources.ResourceLoader) *tools.Registry {
	reg := tools.NewRegistry()
	for _, def := range a.toolReg.Definitions() {
		if a.isToolAllowed(def.Name) {
			reg.Register(a.toolReg.Get(def.Name))
		}
	}
	if len(res.Skills) > 0 && a.isToolAllowed(tools.ToolSkill) {
		reg.Register(tools.Skill(loader.ReadSkill))
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
			reg.Register(tools.Schedule(adapter, sessionID))
		}
		if a.isToolAllowed(tools.ToolScheduleList) {
			reg.Register(tools.ScheduleList(adapter))
		}
		if a.isToolAllowed(tools.ToolScheduleDelete) {
			reg.Register(tools.ScheduleDelete(adapter))
		}
	}
	// Subagent tool — only if allowed (excluded for sub-agents themselves)
	if a.isToolAllowed(tools.ToolSubagent) {
		// Capture current settings in a closure — Agent has zero knowledge of sub-agent mechanics
		parentA := a
		executor := func(ctx context.Context, prompt string) (string, error) {
			// Create ephemeral sub-agent inheriting parent settings. It reuses the
			// parent's MCP tools (via Tools) WITHOUT spawning its own MCP processes
			// (EnableMCPs stays false). It is forbidden from launching further
			// subagents (DisallowedTools) to prevent recursion.
			subAgent := New(AgentOptions{
				ThinkingLevel: parentA.thinkingLevel,
				SystemPrompt:  subagentSystemPrompt,
				MaxTurns:      parentA.maxTurns,
				MaxTokens:     parentA.maxTokens,
				Store:         store.NewInMemoryStore(),
				// Each subagent gets its OWN loader instance — FileResourceLoader is not goroutine-safe
				ResourceLoader: resources.NewFileResourceLoader(cwd),
				// Subagents can't launch further subagents (no recursion) and get
				// READ-ONLY memory: they may recall context (MemoSearch) but
				// not write or delete — only the parent agent curates what persists,
				// avoiding noisy/conflicting writes from ephemeral subagents.
				// Schedule management is parent-only too: like the MCP manager (which
				// runs only in the parent), the scheduler engine and its tools belong
				// to the root agent. Subagents get neither the engine (EnableScheduler
				// stays false) nor the Schedule* tools (disallowed).
				DisallowedTools: []string{
					tools.ToolSubagent, tools.ToolMemoWrite, tools.ToolMemoDelete,
					tools.ToolSchedule, tools.ToolScheduleList, tools.ToolScheduleDelete,
				},
				Tools:        parentA.MCPTools(),
				sharedMemory: parentA.memStore, // share the parent's store (read-only for subagents; not closed by the subagent)
			})
			sess, err := subAgent.NewSession(cwd, model)
			if err != nil {
				return "", fmt.Errorf("sub-agent: %w", err)
			}
			defer sess.Close()
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
			sess.Prompt(ctx, prompt)
			select {
			case err := <-done:
				return strings.TrimSpace(textBuf.String()), err
			case <-ctx.Done():
				return strings.TrimSpace(textBuf.String()), ctx.Err()
			}
		}
		reg.Register(tools.Subagent(executor))
	}
	return reg
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

func (a *Agent) buildSystemPrompt(cwd string, res *resources.Resources) string {
	var b strings.Builder

	if res.SystemMD != "" {
		b.WriteString(res.SystemMD)
	} else {
		b.WriteString(a.systemPrompt)
	}

	if len(res.Skills) > 0 {
		b.WriteString("\n\n## Available Skills\n\nSkills are specialized guides for specific tasks. When a task matches a skill, load it with the Skill tool before starting — it contains workflows and constraints you must follow.\n\n")
		for _, s := range res.Skills {
			b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
		}
	}

	if a.memStore != nil {
		b.WriteString("\n\n## Memory\n\nYou have persistent, project-scoped memory that carries over between sessions. At the start of a task — or whenever you lack context about earlier work — use MemoSearch with relevant keywords to recover prior decisions, conventions, and context. Save durable, high-value insights with MemoWrite (never transient task state), and remove obsolete ones with MemoDelete.")
	}

	if a.schedStore != nil {
		b.WriteString("\n\n## Scheduling\n\nYou can schedule prompts to run automatically on a recurring cron schedule. Use Schedule to create or update one, ScheduleList to review what's scheduled and how often it has run, and ScheduleDelete to remove one. Schedule work the user wants done repeatedly on a cadence; the prompt runs later exactly as if the user sent it.")
	}

	b.WriteString(fmt.Sprintf("\n\n## Working Directory\n\n%s\n", cwd))

	if res.AgentsMD != "" {
		b.WriteString("\n\n## Project Context\n\n")
		b.WriteString(res.AgentsMD)
	}

	// Caller-supplied directives (e.g. a transport's capabilities). Appended last
	// so they can reference everything above.
	for _, d := range a.opts.Directives {
		if d = strings.TrimSpace(d); d != "" {
			b.WriteString("\n\n")
			b.WriteString(d)
		}
	}

	return b.String()
}

// defaultSessionName generates the initial session name — date + time.
// Replaced automatically by the first user message after Prompt() is called.
func defaultSessionName(t time.Time) string {
	return "New Session " + t.Format("2006-01-02 15:04")
}

// isDefaultSessionName returns true if the name matches the auto-generated date format.

func defaultTools() *tools.Registry {
	r := tools.NewRegistry()
	r.Register(tools.Bash())
	r.Register(tools.ReadFile())
	r.Register(tools.WriteFile())
	r.Register(tools.Edit())
	r.Register(tools.Fetch())
	return r
}
