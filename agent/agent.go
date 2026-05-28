package agent

import (
	"fmt"

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
	maxLoops       int
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
	MaxLoops     int    // max ReAct iterations per turn — default: 25
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

	if opts.MaxLoops <= 0 {
		opts.MaxLoops = 25
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
		maxLoops:       opts.MaxLoops,
		maxTokens:      opts.MaxTokens,
	}, nil
}

// NewSession creates a fresh session for a working directory.
func (a *Agent) NewSession(cwd string) (*Session, error) {
	res, err := a.resourceLoader.Load(cwd)
	if err != nil {
		return nil, fmt.Errorf("load resources: %w", err)
	}

	systemPrompt := a.buildSystemPrompt(res)

	// Clone base tools and inject read_skill if skills were discovered
	sessionTools := a.toolReg.Clone()
	if len(res.Skills) > 0 {
		def, execFn := tools.ReadSkill(res)
		sessionTools.Register(tools.Tool{Def: def, Execute: execFn})
	}

	id := uuid.New().String()
	storeInst, err := a.store.Create(id, cwd)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	return newSession(id, cwd, "", storeInst,
		a.provider, a.model, a.thinkingLevel,
		sessionTools, systemPrompt,
		a.maxLoops, a.maxTokens), nil
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
		a.maxLoops, a.maxTokens), nil
}

// ── Internal helpers ────────────────────────────────────────────────────

func (a *Agent) buildSystemPrompt(res *resources.Resources) string {
	prompt := a.systemPrompt

	if res.AgentsMD != "" {
		prompt += "\n\n---\n\n" + res.AgentsMD
	}

	if len(res.Skills) > 0 {
		prompt += "\n\n## Available Skills\n\n"
		for _, s := range res.Skills {
			prompt += fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description)
		}
		prompt += "\nUse the `read_skill` tool to load full instructions for a skill.\n"
	}

	return prompt
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


