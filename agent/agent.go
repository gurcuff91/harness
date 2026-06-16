package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/providers"
	"github.com/gurcuff91/harness/types"
)

// ── Agent ────────────────────────────────────────────────────────────────

// Agent is a pure factory — it holds global config and spawns Sessions.
// It has zero knowledge of providers, credentials, or which providers are active.
// The caller is responsible for ensuring the provider is active before NewSession().
type Agent struct {
	opts           AgentOptions // original opts — used by Subagent tool to clone
	toolReg        *tools.Registry
	allowedTools   []string
	store          store.SessionStoreManager
	resourceLoader resources.ResourceLoader // nil = FileResourceLoader(cwd) per session
	thinkingLevel  string
	systemPrompt   string
	maxTurns       int
	maxTokens      int // 0 = resolved from ModelMeta in NewSession
}

// AgentOptions configures a new Agent.
type AgentOptions struct {
	// ── Thinking ─────────────────────────────────────────────────────────
	ThinkingLevel string // "disable"|"low"|"medium"|"high"|"xhigh"

	// ── Behavior ─────────────────────────────────────────────────────────
	SystemPrompt string // base system prompt for all sessions
	MaxTurns     int    // max ReAct iterations per turn — default: 25
	MaxTokens    int    // max output tokens — default: model's MaxTokens from ModelMeta

	// ── Tools ────────────────────────────────────────────────────────────
	ExtraTools   []tools.Tool // additional tools (defaults always included)
	AllowedTools []string     // tool names allowed — empty = all

	// ── Infrastructure (optional) ────────────────────────────────────────
	Store          store.SessionStoreManager // default: InMemorySessionStoreManager
	ResourceLoader resources.ResourceLoader  // default: FileResourceLoader(cwd) per session
	//                                         // pass NilLoader{} to disable discovery
}

// New creates a new Agent. Never fails — provider is resolved per session.
func New(opts AgentOptions) *Agent {
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = 25
	}
	if opts.SystemPrompt == "" {
		opts.SystemPrompt = defaultSystemPrompt
	}
	if opts.Store == nil {
		// Default: file-backed store in ~/.harness/agent/sessions/
		// Falls back to in-memory if filesystem is unavailable
		if fs, err := store.NewFileSessionStoreManager(""); err == nil {
			opts.Store = fs
		} else {
			opts.Store = store.NewInMemorySessionStoreManager()
		}
	}

	reg := defaultTools()
	for _, t := range opts.ExtraTools {
		reg.Register(t)
	}

	return &Agent{
		opts:           opts,
		toolReg:        reg,
		allowedTools:   opts.AllowedTools,
		store:          opts.Store,
		resourceLoader: opts.ResourceLoader,
		thinkingLevel:  opts.ThinkingLevel,
		systemPrompt:   opts.SystemPrompt,
		maxTurns:       opts.MaxTurns,
		maxTokens:      opts.MaxTokens,
	}
}

// Options returns the original configuration — used by the Subagent tool to clone.
func (a *Agent) Options() AgentOptions {
	return a.opts
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

	sessionTools := a.buildSessionTools(cwd, model, res, loader)
	systemPrompt := a.buildSystemPrompt(cwd, res)

	now := time.Now()
	thinking := a.thinkingLevel
	if thinking == "" {
		thinking = "off"
	}
	meta := store.SessionMeta{
		ID:           uuid.New().String(),
		CWD:          cwd,
		Name:         defaultSessionName(now),
		Model:        model,
		Thinking:     thinking,
		CreatedAt:    now,
		LastActiveAt: now,
	}
	storeInst, err := a.store.Create(meta)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	return newSession(storeInst,
		provider, modelID, a.thinkingLevel,
		sessionTools, systemPrompt,
		a.maxTurns, maxTokens,
		res.Skills, loader.ReadSkill), nil
}

// ResumeSession reopens an existing session, fully restoring its state.
func (a *Agent) ResumeSession(sessionID string) (*Session, error) {
	storeInst, err := a.store.Open(sessionID)
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
	var readSkill func(string) (string, error)
	if res != nil {
		skills = res.Skills
		readSkill = loader.ReadSkill
	}

	return newSession(storeInst,
		provider, modelID, thinkingLvl,
		a.buildSessionTools(cwd, meta.Model, res, loader),
		a.buildSystemPrompt(cwd, res),
		a.maxTurns, maxTokens,
		skills, readSkill), nil
}

// ── Session management ───────────────────────────────────────────────────

func (a *Agent) ListSessions(cwd string) ([]store.SessionMeta, error) {
	return a.store.List(cwd)
}

func (a *Agent) ListAllSessions() ([]store.SessionMeta, error) {
	return a.store.ListAll()
}

func (a *Agent) DeleteSession(sessionID string) error {
	return a.store.Delete(sessionID)
}

func (a *Agent) RenameSession(sessionID, name string) error {
	return a.store.Rename(sessionID, name)
}

// ── Internal helpers ─────────────────────────────────────────────────────

func (a *Agent) buildSessionTools(cwd, model string, res *resources.Resources, loader resources.ResourceLoader) *tools.Registry {
	reg := tools.NewRegistry()
	for _, def := range a.toolReg.Definitions() {
		if a.isToolAllowed(def.Name) {
			reg.Register(a.toolReg.Get(def.Name))
		}
	}
	if len(res.Skills) > 0 && a.isToolAllowed(tools.ToolSkill) {
		reg.Register(tools.Skill(loader.ReadSkill))
	}
	// Subagent tool — only if allowed (excluded for sub-agents themselves)
	if a.isToolAllowed(tools.ToolSubagent) {
		// Capture current settings in a closure — Agent has zero knowledge of sub-agent mechanics
		parentA := a
		executor := func(ctx context.Context, prompt string) (string, error) {
			// Build allowed tools list: everything except Subagent (no recursion)
			var allowed []string
			for _, t := range defaultAllTools() {
				if t != tools.ToolSubagent {
					allowed = append(allowed, t)
				}
			}
			// Create ephemeral sub-agent inheriting parent settings
			subAgent := New(AgentOptions{
				ThinkingLevel: parentA.thinkingLevel,
				SystemPrompt:  subagentSystemPrompt,
				MaxTurns:      parentA.maxTurns,
				MaxTokens:     parentA.maxTokens,
				Store:         store.NewInMemorySessionStoreManager(),
				// Each subagent gets its OWN loader instance — FileResourceLoader is not goroutine-safe
				ResourceLoader: resources.NewFileResourceLoader(cwd),
				AllowedTools:  allowed,
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

	if res.SystemMD != "" {
		b.WriteString(res.SystemMD)
	} else {
		b.WriteString(a.systemPrompt)
	}

	if len(res.Skills) > 0 {
		b.WriteString("\n\n## Available Skills\n\n")
		for _, s := range res.Skills {
			b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
		}
	}

	b.WriteString(fmt.Sprintf("\n\n## Working Directory\n\n%s\n", cwd))

	if res.AgentsMD != "" {
		b.WriteString("\n\n## Project Context\n\n")
		b.WriteString(res.AgentsMD)
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

// defaultAllTools returns the names of all default tools (excluding Subagent).
func defaultAllTools() []string {
	return []string{
		tools.ToolBash,
		tools.ToolRead,
		tools.ToolWrite,
		tools.ToolEdit,
		tools.ToolFetch,
		tools.ToolSkill,
		tools.ToolSubagent,
	}
}

