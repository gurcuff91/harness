package agent

import (
	"fmt"
	"strings"

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
	store          store.SessionStore
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
	MaxTokens    int    // max output tokens per LLM call — default: 8192

	// ── Tools ─────────────────────────────────────────────────────────────
	ExtraTools []tools.Tool // additional tools (default tools always included)

	// ── Infrastructure (optional) ─────────────────────────────────────────
	Store          store.SessionStore       // default: InMemoryStore
	ResourceLoader resources.ResourceLoader // default: NilLoader (FileLoader coming soon)
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
		opts.MaxTokens = 8192
	}
	if opts.Store == nil {
		opts.Store = store.NewInMemoryStore()
	}
	if opts.ResourceLoader == nil {
		opts.ResourceLoader = resources.NilLoader{}
	}

	// Build base tool registry: defaults + extras
	reg := defaultTools()
	for _, t := range opts.ExtraTools {
		reg.Register(t)
	}

	return &Agent{
		provider:       provider,
		toolReg:        reg,
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
	res, err := a.resourceLoader.Load()
	if err != nil {
		return nil, fmt.Errorf("load resources: %w", err)
	}

	// Clone base tools and inject skill tool only if skills were discovered
	sessionTools := a.toolReg.Clone()
	if len(res.Skills) > 0 {
		def, execFn := tools.Skill(a.resourceLoader.ReadSkill)
		sessionTools.Register(tools.Tool{Def: def, Execute: execFn})
	}

	systemPrompt := a.buildSystemPrompt(cwd, res)

	id := uuid.New().String()
	storeInst, err := a.store.Create(id, cwd)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	return newSession(id, cwd, "", storeInst,
		a.provider, a.model, a.thinkingLevel,
		sessionTools, systemPrompt,
		a.maxTurns, a.maxTokens), nil
}

// ResumeSession reopens an existing session by ID.
func (a *Agent) ResumeSession(sessionID string) (*Session, error) {
	storeInst, err := a.store.Open(sessionID)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if storeInst == nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	return newSession(sessionID, "", "", storeInst,
		a.provider, a.model, a.thinkingLevel,
		a.toolReg.Clone(), a.systemPrompt,
		a.maxTurns, a.maxTokens), nil
}

// ── Internal helpers ────────────────────────────────────────────────────

func (a *Agent) buildSystemPrompt(cwd string, res *resources.Resources) string {
	var b strings.Builder

	// 1. Identity — SYSTEM.md replaces completely
	if res.SystemMD != "" {
		b.WriteString(res.SystemMD)
	} else {
		b.WriteString(a.systemPrompt)
	}

	// 2. Tool policy — always present, survives SYSTEM.md override
	b.WriteString("\n\nDo not use bash for file operations when dedicated file tools are available.")

	// 3. Available Skills — only if skills were discovered
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


