package agent

import (
	"context"
	"encoding/json"
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
	maxIterations   int
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

	// Graceful shutdown (idempotent Close).
	closeOnce sync.Once
	closeErr  error
}

// AgentOptions configures a new Agent.
type AgentOptions struct {
	// ── Thinking ─────────────────────────────────────────────────────────
	ThinkingLevel string // "disable"|"low"|"medium"|"high"|"xhigh"

	// ── Behavior ─────────────────────────────────────────────────────────
	SystemPrompt  string   // base system prompt for all sessions
	Directives    []string // extra instruction blocks appended to the system prompt (e.g. transport-specific capabilities)
	MaxIterations int      // max ReAct iterations per turn — default: 50 (defaultMaxIterations)
	MaxTokens     int      // max output tokens — default: model's MaxTokens from ModelMeta

	// ── Tools ────────────────────────────────────────────────────────────
	Tools           []tools.Tool // additional tools (defaults always included)
	DisallowedTools []string     // tool names to exclude — empty = all allowed
	EnableMCPs      bool         // spawn & connect configured MCP servers (root agent only)

	// ── Infrastructure (optional) ────────────────────────────────────────
	Store          store.SessionStore       // default: in-memory
	ResourceLoader resources.ResourceLoader // default: FileResourceLoader(cwd) per session
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

	// EnableColleagues turns on the ColleagueList/ColleagueAsk tools: the agent
	// can discover OTHER running harness server instances on this machine (via
	// ~/.harness/instances.json, see agent/colleague) and delegate a prompt to
	// one of them over HTTP — each colleague answers with its own model, MCPs,
	// and project context, not the caller's. Off by default; disabled for
	// subagents and one-shot CLI commands regardless of this flag.
	EnableColleagues bool
}

// defaultMaxIterations is the fallback used when AgentOptions.MaxIterations
// isn't set. It's the SDK/one-shot-command default — generous enough for a
// real multi-step task without being unbounded; interactive transports (TUI,
// serve, Telegram) that expect longer, more complex work override it higher
// (see internal/cli/agent.go), and subagents cap it lower via
// subagentMaxIterations (see the Subagent tool wiring below).
const defaultMaxIterations = 50

// subagentMaxIterations caps a subagent's ReAct iterations regardless of the
// parent's own limit. A subagent is a focused, delegated task (see
// subagentSystemPrompt), not the primary agent driving a long, multi-part
// session — it shouldn't need as much room as a parent running with
// interactiveMaxIterations (120), and capping it means a runaway subagent
// gets cut off (with the usual progress-summary fallback) well before it
// burns through a comparable budget without the parent knowing until it
// finally returns.
const subagentMaxIterations = 50

// New creates a new Agent. Never fails — provider is resolved per session.
func New(opts AgentOptions) *Agent {
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = defaultMaxIterations
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

	// Fetch is the only agent-level built-in seeded here: it has no cwd
	// dependency (HTTP requests, not local file/process access), unlike
	// Bash/Read/Write/Edit — those are built per-session, with that session's
	// cwd, in buildSessionTools (the single place session-scoped tools come
	// from; see its comment).
	reg := tools.NewRegistry()
	reg.Register(tools.Fetch())

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
		maxIterations:   opts.MaxIterations,
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
// not currently active, it is auto-resumed from disk so scheduled prompts are
// never lost across process restarts. The engine still records the run.
//
// owner == "" is the single-session fallback (e.g. the TUI): if exactly one
// session is active, it receives the prompt.
func (a *Agent) fireScheduledPrompt(slug, prompt, owner string) {
	sess := a.resolveScheduledSession(owner)
	if sess == nil && owner != "" {
		// Session not active — auto-resume from disk so the prompt runs.
		// If the session no longer exists on disk, drop silently.
		sess, _ = a.ResumeSession(owner)
	}
	if sess != nil {
		sess.Prompt(context.Background(), prompt, PromptWithOriginScheduled())
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

// RegisterTool adds a tool to the agent's registry so all future sessions
// created by this agent include it. Must be called before NewSession/ResumeSession.
// Idempotent: re-registering the same name replaces the previous entry.
func (a *Agent) RegisterTool(t tools.Tool) {
	a.toolReg.Register(t)
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

// MaxIterations returns the max ReAct iterations per turn this agent creates
// sessions with (AgentOptions.MaxIterations, default 50). Every session gets
// the same value at creation, so this is the right fallback for a session
// that isn't currently active (no live *Session to ask directly).
func (a *Agent) MaxIterations() int { return a.maxIterations }

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
// store (which they must not close). Idempotent (sync.Once) and nil-safe; both
// resources are released even if one fails.
func (a *Agent) Close() error {
	a.closeOnce.Do(func() {
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
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}

// newLoader returns the ResourceLoader for a session about to be built: a
// fresh FileResourceLoader(cwd) if no custom loader was configured, or an
// independent Copy() of a.resourceLoader otherwise — never a.resourceLoader
// itself. Each session (and, via buildSessionTools' Subagent executor, each
// sub-agent it spawns) gets its own instance because Load() may build
// per-call state in place (e.g. FileResourceLoader's skill index), which
// isn't safe to share across sessions calling Load() concurrently — a root
// Agent serving multiple long-lived sessions (TUI/Telegram/ACP) with a
// custom loader configured via harness.WithResourceLoader would otherwise
// have every session racing on the same instance's internal state.
func (a *Agent) newLoader(cwd string) resources.ResourceLoader {
	if a.resourceLoader == nil {
		return resources.NewFileResourceLoader(cwd)
	}
	return a.resourceLoader.Copy()
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
	loader := a.newLoader(cwd)
	res, err := loader.Load()
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
	sessionTools, tl := a.buildSessionTools(sessionID, cwd, &sess, res, loader)
	systemPrompt, pl := a.buildSystemPrompt(cwd, res)

	meta := store.SessionMeta{
		ID:           sessionID,
		CWD:          cwd,
		Name:         defaultSessionName(now),
		Model:        model,
		Thinking:     a.thinkingLevel,
		CreatedAt:    now,
		LastActiveAt: now,
	}
	storeInst, err := store.CreateSession(a.store, meta)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	sess = newSession(storeInst,
		provider, modelID, a.thinkingLevel,
		sessionTools, tl, systemPrompt, pl,
		a.maxIterations, maxTokens,
		res.Skills, loader.ReadSkill,
		a.memStore != nil)
	sess.agent = a
	a.registerSession(sess)
	return sess, nil
}

// ResumeSession reopens an existing session, fully restoring its state. If the
// session is already active (live in the agent's registry), it returns the
// existing handle without reloading from disk — making this idempotent and safe
// to call from multiple paths (transport reconnect, scheduler auto-resume, …).
func (a *Agent) ResumeSession(sessionID string) (*Session, error) {
	// Fast path: already active — return the live handle.
	a.sessMu.Lock()
	if sess, ok := a.activeSessions[sessionID]; ok {
		a.sessMu.Unlock()
		return sess, nil
	}
	a.sessMu.Unlock()

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
	loader := a.newLoader(cwd)
	res, _ := loader.Load()
	var skills []resources.SkillInfo
	var readSkill func(string) (content string, dir string, err error)
	if res != nil {
		skills = res.Skills
		readSkill = loader.ReadSkill
	}

	var sess *Session
	resumeTools, tl := a.buildSessionTools(meta.ID, cwd, &sess, res, loader)
	resumePrompt, pl := a.buildSystemPrompt(cwd, res)
	sess = newSession(storeInst,
		provider, modelID, thinkingLvl,
		resumeTools, tl, resumePrompt, pl,
		a.maxIterations, maxTokens,
		skills, readSkill,
		a.memStore != nil)
	sess.agent = a
	a.registerSession(sess)
	return sess, nil
}

// ── Session management ───────────────────────────────────────────────────

// ForkSession creates a new session that is an exact copy of sessionID at this
// moment: same CWD, model, thinking, compaction state, stats, and full message
// history. The fork gets a new ID and fresh timestamps. The parent is unchanged.
// Returns ErrBusy (via the store layer) if the parent turn is in flight.
func (a *Agent) ForkSession(sessionID string) (*Session, error) {
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

	// Build a live agent.Session on top of the fork store.
	meta := forkStore.Meta()
	provider, modelID, err := providers.Resolve(meta.Model)
	if err != nil {
		return nil, fmt.Errorf("fork: resolve provider: %w", err)
	}

	thinkingLvl := meta.Thinking
	maxTokens := a.maxTokens
	if maxTokens == 0 {
		if m := provider.ModelMeta(modelID); m != nil && m.MaxTokens > 0 {
			maxTokens = m.MaxTokens
		} else {
			maxTokens = 32000
		}
	}

	cwd := meta.CWD
	loader := a.newLoader(cwd)
	res, _ := loader.Load()
	var skills []resources.SkillInfo
	var readSkill func(string) (string, string, error)
	if res != nil {
		skills = res.Skills
		readSkill = loader.ReadSkill
	}

	var sess *Session
	forkTools, tl := a.buildSessionTools(meta.ID, cwd, &sess, res, loader)
	forkPrompt, pl := a.buildSystemPrompt(cwd, res)
	sess = newSession(forkStore,
		provider, modelID, thinkingLvl,
		forkTools, tl, forkPrompt, pl,
		a.maxIterations, maxTokens,
		skills, readSkill,
		a.memStore != nil)
	sess.agent = a
	a.registerSession(sess)
	return sess, nil
}

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
// (*sessRef).CurrentModel() at EXECUTION time — reflecting any SwitchModel
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

func (a *Agent) buildSessionTools(sessionID, cwd string, sessRef **Session, res *resources.Resources, loader resources.ResourceLoader) (*tools.Registry, toolLens) {
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
			// owner = this session: it only sees its own schedules.
			reg.Register(tools.ScheduleList(adapter, sessionID))
		}
		if a.isToolAllowed(tools.ToolScheduleDelete) {
			reg.Register(tools.ScheduleDelete(adapter, sessionID))
		}
	}
	// Colleague tools — discover and delegate to OTHER running harness
	// instances on this machine, backed by ~/.harness/instances.json (owned
	// and written by server; these tools read the same file path
	// directly with their own minimal parser — see agent/tools/colleague.go).
	// Off unless EnableColleagues.
	if a.opts.EnableColleagues {
		if a.isToolAllowed(tools.ToolColleagueList) {
			reg.Register(tools.ColleagueList())
		}
		if a.isToolAllowed(tools.ToolColleagueAsk) {
			reg.Register(tools.ColleagueAsk())
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
				MaxIterations: min(parentA.maxIterations, subagentMaxIterations),
				MaxTokens:     parentA.maxTokens,
				Store:         store.NewInMemoryStore(),
				// Each subagent gets its OWN loader instance, via Copy() — never
				// the parent's own loader value directly. Two reasons: (1) a
				// loader's Load() may build per-call state in place (e.g.
				// FileResourceLoader's skill index), which isn't safe to run
				// concurrently with the parent session's own Load() on a SHARED
				// instance; (2) hardcoding resources.NewFileResourceLoader(cwd)
				// here — as this used to do — silently ignored whatever
				// ResourceLoader implementation the parent Agent was actually
				// configured with (e.g. a custom one injected via
				// harness.WithResourceLoader, loading skills from a database or
				// object store): every sub-agent would see a completely
				// different, filesystem-only context than the parent session
				// did. Copy() (see ResourceLoader's doc comment) returns a fresh
				// instance of the SAME implementation and configuration.
				ResourceLoader: loader.Copy(),
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
			// Read the CURRENT model at execution time, not the one captured
			// when this closure was built — see buildSessionTools' sessRef
			// doc comment for why that distinction is the whole point here.
			// (*sessRef) is guaranteed non-nil by the time this executor can
			// ever run: the tool it belongs to isn't reachable by the model
			// until the owning session's Prompt() has been called at least
			// once, which is well after every call site below assigns it.
			currentModel := (*sessRef).CurrentModel()
			sess, err := subAgent.NewSession(cwd, currentModel)
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
			return awaitSubagentResult(ctx, done, &textBuf)
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

func (a *Agent) buildSystemPrompt(cwd string, res *resources.Resources) (string, promptLens) {
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

	if a.schedStore != nil {
		b.WriteString("\n\n## Scheduling\n\nYou can schedule prompts to run automatically on a recurring cron schedule. Use Schedule to create or update one, ScheduleList to review what's scheduled and how often it has run, and ScheduleDelete to remove one. Schedule work the user wants done repeatedly on a cadence; the prompt runs later exactly as if the user sent it.")
	}

	if a.opts.EnableColleagues {
		b.WriteString("\n\n## Colleagues\n\nYou are not the only agent running. Other colleague instances may be reachable right now, each with its own model, tools, and project context — use them instead of trying to do everything yourself.\n\nUse ColleagueList to see who's reachable, then ColleagueAsk to delegate to one by name. Each colleague has an environment (extra capabilities that colleague has, which you may not) and a working directory (the project it has context on):\n\n- Delegate by environment when the task needs a capability you don't have — a colleague's environment may let it do things you can't from here.\n- Delegate by working directory when the task belongs to a different project than yours. A colleague in the SAME project as you is also worth delegating to for a substantial, self-contained task — they're an independent agent that can co-work with you on it; each delegation is a fresh session, not a shared memory of past exchanges.\n\nNeither is restrictive — combine them. A colleague in a different project AND environment than yours can still be exactly the right one for the task.")
	}

	b.WriteString(fmt.Sprintf("\n\n## Working Directory\n\n%s\n\nPrefer absolute paths when reading, writing, or editing files, and when running Bash commands that touch specific files. Relative paths resolve against this working directory, but an absolute path is unambiguous.\n", cwd))

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

	full := b.String()
	return full, promptLens{total: len(full)}
}

// defaultSessionName generates the initial session name — date + time.
// Replaced automatically by the first user message after Prompt() is called.
func defaultSessionName(t time.Time) string {
	return "New Session " + t.Format("2006-01-02 15:04")
}

// isDefaultSessionName returns true if the name matches the auto-generated date format.
