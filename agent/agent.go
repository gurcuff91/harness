package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/providers"
	pllm "github.com/gurcuff91/harness/providers/llm"
)

// ── Agent ───────────────────────────────────────────────────────────────

// Agent is the entry point and factory. It holds global config and spawns Sessions.
type Agent struct {
	provider       pllm.Provider
	toolReg        *tools.Registry
	allowedTools   []string // empty = all tools allowed
	store          store.SessionStoreManager
	resourceLoader resources.ResourceLoader
	model          string // bare model ID
	thinkingLevel  string
	systemPrompt   string
	maxTurns       int
	maxTokens      int
}

// AgentOptions configures a new Agent.
type AgentOptions struct {
	// ── Required ──────────────────────────────────────────────────────────
	Model string // "provider/model" e.g. "opencode-go/deepseek-v4-pro"
	//            // provider is resolved internally via providers.Get()

	// ── Thinking ──────────────────────────────────────────────────────────
	ThinkingLevel string // "disable"|"low"|"medium"|"high"|"xhigh" — default: ""

	// ── Behavior ──────────────────────────────────────────────────────────
	SystemPrompt string // base system prompt for all sessions
	MaxTurns     int    // max ReAct iterations per turn — default: 25
	MaxTokens    int    // max output tokens per LLM call — default: model's MaxTokens from ModelMeta

	// ── Tools ─────────────────────────────────────────────────────────────
	ExtraTools   []tools.Tool // additional tools (default tools always included)
	AllowedTools []string     // tool names the agent may use — empty = all allowed

	// ── Infrastructure (optional) ─────────────────────────────────────────
	Store          store.SessionStoreManager       // default: InMemoryStore
	ResourceLoader resources.ResourceLoader // default: FileResourceLoader(cwd) per session
	//                                       // pass NilLoader{} to disable resource discovery
}

// New creates a new Agent.
// Returns error if the provider cannot be resolved or has no credentials.
func New(opts AgentOptions) (*Agent, error) {
	// Resolve provider + validate model in one step
	provider, modelID, err := providers.Resolve(opts.Model)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	if opts.MaxTurns <= 0 {
		opts.MaxTurns = 25
	}
	if opts.MaxTokens <= 0 {
		// Use the model's actual max output tokens — no artificial cap
		if meta := provider.ModelMeta(modelID); meta != nil && meta.MaxTokens > 0 {
			opts.MaxTokens = meta.MaxTokens
		} else {
			opts.MaxTokens = 32000 // safe fallback
		}
	}
	if opts.Store == nil {
		opts.Store = store.NewInMemorySessionStoreManager()
	}
	// ResourceLoader nil = FileResourceLoader created per session with session's cwd
	// Pass resources.NilLoader{} explicitly to disable resource discovery

	// Build base tool registry: defaults + extras
	reg := defaultTools()
	for _, t := range opts.ExtraTools {
		reg.Register(t)
	}

	return &Agent{
		provider:       provider,
		toolReg:        reg,
		allowedTools:   opts.AllowedTools,
		store:          opts.Store,
		resourceLoader: opts.ResourceLoader,
		model:          modelID,
		thinkingLevel:  opts.ThinkingLevel,
		systemPrompt:   opts.SystemPrompt,
		maxTurns:       opts.MaxTurns,
		maxTokens:      opts.MaxTokens,
	}, nil
}

// NewSession creates a fresh session for a working directory.
func (a *Agent) NewSession(cwd string) (*Session, error) {
	// Use FileResourceLoader with this session's cwd by default
	loader := a.resourceLoader
	if loader == nil {
		loader = resources.NewFileResourceLoader(cwd)
	}
	res, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("load resources: %w", err)
	}

	sessionTools := a.buildSessionTools(res, loader)

	systemPrompt := a.buildSystemPrompt(cwd, res)

	now := time.Now()
	meta := store.SessionMeta{
		ID:           uuid.New().String(),
		CWD:          cwd,
		Model:        a.provider.Name() + "/" + a.model,
		Thinking:     a.thinkingLevel,
		CreatedAt:    now,
		LastActiveAt: now,
	}
	storeInst, err := a.store.Create(meta)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	return newSession(storeInst,
		a.provider, a.model, a.thinkingLevel,
		sessionTools, systemPrompt,
		a.maxTurns, a.maxTokens), nil
}

// ListSessions returns all sessions for a given working directory.
func (a *Agent) ListSessions(cwd string) ([]store.SessionMeta, error) {
	return a.store.List(cwd)
}

// ListAllSessions returns all sessions across all directories.
func (a *Agent) ListAllSessions() ([]store.SessionMeta, error) {
	return a.store.ListAll()
}

// DeleteSession removes a session permanently.
func (a *Agent) DeleteSession(sessionID string) error {
	return a.store.Delete(sessionID)
}

// RenameSession sets a friendly name for a session.
func (a *Agent) RenameSession(sessionID, name string) error {
	return a.store.Rename(sessionID, name)
}

// ResumeSession reopens an existing session, fully restoring its state:
// cwd, model, thinking level, and accumulated stats.
func (a *Agent) ResumeSession(sessionID string) (*Session, error) {
	storeInst, err := a.store.Open(sessionID)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if storeInst == nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	meta := storeInst.Meta()

	// Restore provider+model from the session's last known model
	// Fall back to agent default if provider is no longer available
	provider := a.provider
	modelID := a.model
	if meta.Model != "" {
		if p, id, err := providers.Resolve(meta.Model); err == nil {
			provider = p
			modelID = id
		}
	}

	thinkingLvl := a.thinkingLevel
	if meta.Thinking != "" {
		thinkingLvl = meta.Thinking
	}

	// Rebuild resources and tools for the session's cwd
	cwd := meta.CWD
	loader := a.resourceLoader
	if loader == nil {
		loader = resources.NewFileResourceLoader(cwd)
	}
	res, _ := loader.Load()
	sessionTools := a.buildSessionTools(res, loader)
	systemPrompt := a.buildSystemPrompt(cwd, res)

	return newSession(storeInst,
		provider, modelID, thinkingLvl,
		sessionTools, systemPrompt,
		a.maxTurns, a.maxTokens), nil
}

// ── Internal helpers ────────────────────────────────────────────────────

// buildSessionTools constructs the tool registry for a session.
// Applies AllowedTools filter to all tools (built-in, extra, and skill).
func (a *Agent) buildSessionTools(res *resources.Resources, loader resources.ResourceLoader) *tools.Registry {
	reg := tools.NewRegistry()

	// Add tools from the base registry — filtered by AllowedTools
	for _, def := range a.toolReg.Definitions() {
		if a.isToolAllowed(def.Name) {
			reg.Register(a.toolReg.Get(def.Name))
		}
	}

	// Inject skill tool only if skills discovered AND skill is allowed
	if len(res.Skills) > 0 && a.isToolAllowed("skill") {
		def, execFn := tools.Skill(loader.ReadSkill)
		reg.Register(tools.Tool{Def: def, Execute: execFn})
	}

	return reg
}

// isToolAllowed returns true if the tool is in AllowedTools, or if AllowedTools is empty.
func (a *Agent) isToolAllowed(name string) bool {
	if len(a.allowedTools) == 0 {
		return true
	}
	for _, n := range a.allowedTools {
		if n == name {
			return true
		}
	}
	return false
}

func (a *Agent) buildSystemPrompt(cwd string, res *resources.Resources) string {
	var b strings.Builder

	// 1. Identity — SYSTEM.md replaces completely
	if res.SystemMD != "" {
		b.WriteString(res.SystemMD)
	} else {
		b.WriteString(a.systemPrompt)
	}

	// 2. Available Skills — only if skills were discovered
	if len(res.Skills) > 0 {
		b.WriteString("\n\n## Available Skills\n\n")
		for _, s := range res.Skills {
			b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
		}
	}

	// 4. Working Directory — always present
	b.WriteString(fmt.Sprintf("\n\n## Working Directory\n\n%s\n", cwd))

	// 5. Project Context — only if AGENTS.md exists
	if res.AgentsMD != "" {
		b.WriteString("\n\n## Project Context\n\n")
		b.WriteString(res.AgentsMD)
	}

	return b.String()
}

func defaultTools() *tools.Registry {
	r := tools.NewRegistry()
	r.Register(tools.Bash())
	r.Register(tools.ReadFile())
	r.Register(tools.WriteFile())
	r.Register(tools.Edit())
	r.Register(tools.Fetch())
	return r
}


