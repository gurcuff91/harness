# 01 — Agent Design

## Overview

`Agent` is the high-level entry point and factory. It holds global config and spawns `Session` objects. The real core of a conversation is the `Session`.

```
SDK user / transport
        ↓
      Agent          ← factory, config holder
        ↓
     Session         ← core of a conversation
        ↓
   ReAct Loop        ← internal, private
        ↓
  llm.Provider       ← streaming LLM calls
```

---

## Agent

Factory + global config. Creates and resumes sessions.

```go
type Agent struct {
    provider       llm.Provider      // default provider for new sessions
    tools          *tools.Registry   // base tools (no read_skill — injected per session)
    store          SessionStore      // interface — knows how to create SessionStoreInstances
    resourceLoader ResourceLoader    // interface — discovers AGENTS.md, skills, context
    opts           Options
}

type Options struct {
    SystemPrompt string  // base system prompt
    MaxLoops     int     // ReAct loop max iterations (default: 25)
    MaxTokens    int     // max output tokens per LLM call (default: 8192)
}
```

### Constructor

```go
func New(opts AgentOptions) *Agent

type AgentOptions struct {
    Provider       llm.Provider     // required — default model
    Store          SessionStore     // required — persistence backend
    ResourceLoader ResourceLoader   // optional — nil = NilResourceLoader
    Tools          *tools.Registry  // optional — nil = default tools
    SystemPrompt   string
    MaxLoops       int
    MaxTokens      int
}
```

### Methods

```go
// NewSession creates a fresh session for the given working directory.
// Loads resources from ResourceLoader, creates a new SessionStoreInstance,
// injects read_skill tool with closure over loaded resources.
func (a *Agent) NewSession(cwd string) (*Session, error)

// ResumeSession reopens an existing session by ID.
// Restores the provider/model from the last model_change entry in the store.
func (a *Agent) ResumeSession(sessionID string) (*Session, error)
```

### What Agent does NOT do

- Does not manage conversation history
- Does not subscribe to events (that's per-Session)
- Does not know about compaction
- Does not know about session listing/deletion (that's SessionManager, used by transports)

---

## Session

The real core. Manages one conversation — its history, its model, its events.

```go
type Session struct {
    id        string
    cwd       string
    store     SessionStoreInstance  // this session's own store
    resources *Resources            // loaded once at creation, immutable
    provider  llm.Provider          // this session's model (may differ from agent default)
    tools     *tools.Registry       // agent base tools + read_skill injected
    opts      Options
    handler   Handler               // single event subscriber
    mu        sync.Mutex            // serializes turns (one turn at a time)
}
```

### Methods

```go
// Prompt runs one full turn: user message → ReAct loop → response.
// Persists all messages (user, assistant, tool calls, tool results) to store.
func (s *Session) Prompt(ctx context.Context, text string, images []llm.ImageData) (string, error)

// Subscribe registers a handler for all agent events (streaming, tool calls, etc.).
// Replaces any previous handler. Events from this session only.
func (s *Session) Subscribe(h Handler)

// SwitchModel changes the provider/model for this session.
// Persists a model_change entry to the store.
func (s *Session) SwitchModel(provider llm.Provider) error

// SwitchThinking changes the thinking level for this session.
// Persists a thinking_change entry to the store.
func (s *Session) SwitchThinking(level string) error

// Compact runs context compaction: summarizes history via LLM, truncates old messages.
// Persists a compaction entry to the store.
func (s *Session) Compact(ctx context.Context) error

// Rename sets a friendly display name for the session.
// Persists a session_info entry to the store.
func (s *Session) Rename(name string) error

// Meta returns a snapshot of session metadata (id, name, cwd, model, turns, timestamps).
func (s *Session) Meta() SessionMeta

// Close flushes and closes the store.
func (s *Session) Close() error
```

### Internal: ReAct loop

Private to Session. Called by `Prompt()`. Handles:
- Streaming LLM calls via `provider.CompleteStream()`
- Tool execution during stream (on `StreamToolEnd`)
- Emitting events to the subscriber handler
- Persisting each message to the store via `store.AddMessage()`

The system prompt is built **once at session creation** (base + AgentsMD + skills list) and reused for every turn. It never changes during a session.

---

## SessionStore (interface)

Knows how to create and open `SessionStoreInstance` objects.

```go
type SessionStore interface {
    Create(sessionID, cwd string) (SessionStoreInstance, error)
    Open(sessionID string) (SessionStoreInstance, error)
}
```

### Implementations

- `InMemorySessionStore` — creates in-memory instances (tests, SDK no-persist mode)
- `FsSessionStore{Dir: "~/.harness/sessions"}` — creates JSONL files per session

---

## SessionStoreInstance (interface)

One session's persistent log. Append-only.

```go
type SessionStoreInstance interface {
    Messages() []json.RawMessage  // all messages for LLM history reconstruction
    AddMessage(msg json.RawMessage) error
    Close() error
}
```

Note: `Messages()` returns only the messages relevant for LLM context (after last compaction).
Full entry log (including model_change, compaction, etc.) is internal to the implementation.

---

## ResourceLoader (interface)

Discovers context files for a given working directory.

```go
type ResourceLoader interface {
    Load(cwd string) (*Resources, error)
}

type Resources struct {
    SystemPrompt string      // from ~/.harness/AGENTS.md global or override
    AgentsMD     string      // from <cwd>/AGENTS.md or nearest parent
    Skills       []SkillInfo // discovered skills (name + description only)
}

type SkillInfo struct {
    Name        string
    Description string
    Location    string  // absolute path — used by read_skill tool to load content
}
```

### Implementations

- `FileResourceLoader{MaxDepth: 3}` — walks cwd upward looking for AGENTS.md, skills/
- `NilResourceLoader{}` — returns empty Resources (tests, minimal SDK usage)

---

## read_skill tool injection

`read_skill` is NOT in the global tools registry. It's injected per-session with a closure over the loaded resources:

```go
// Inside Agent.NewSession():
resources, _ := a.resourceLoader.Load(cwd)
sessionTools := a.tools.Clone()
sessionTools.Register(tools.ReadSkill(resources))  // closure — knows the skills
```

```go
// tools/skill.go
func ReadSkill(resources *Resources) Tool {
    return Tool{
        Def: llm.ToolDef{
            Name:        "read_skill",
            Description: "Load the full instructions for a skill by name.",
            InputSchema: ...,
        },
        Execute: func(input json.RawMessage) (string, error) {
            // find skill in resources.Skills by name
            // read file at skill.Location
            // return content
        },
    }
}
```

---

## System prompt construction

Built once at `NewSession()`, immutable for the session lifetime:

```
[opts.SystemPrompt — base]

---

[resources.AgentsMD — project context]

---

## Available Skills

- **developer**: Use when starting a new feature...
- **standup-creator**: Creates structured standup messages...

Use the `read_skill` tool to load full instructions for a skill.
```

---

## Session creation flow

```
Agent.NewSession(cwd)
  │
  ├── resourceLoader.Load(cwd)
  │     → Resources{AgentsMD, Skills, SystemPrompt}
  │
  ├── buildSystemPrompt(opts.SystemPrompt, resources)
  │     → immutable string for this session
  │
  ├── store.Create(newUUID(), cwd)
  │     → SessionStoreInstance
  │
  ├── sessionTools = agent.tools.Clone()
  │   sessionTools.Register(ReadSkill(resources))
  │
  └── return &Session{
          id:        uuid,
          cwd:       cwd,
          store:     storeInstance,
          resources: resources,
          provider:  agent.provider,   // default, mutable via SwitchModel
          tools:     sessionTools,
          opts:      agent.opts,
          systemPrompt: builtPrompt,
      }
```

---

## File Structure

```
agent/
├── agent.go          ← Agent struct, New(), NewSession(), ResumeSession()
├── session.go        ← Session struct + Prompt(), Subscribe(), SwitchModel(),
│                       SwitchThinking(), Compact(), Rename(), Meta(), Close()
│                       + ReAct loop (private methods)
├── event.go          ← Event types, Handler — sin cambios
├── manager.go        ← SessionManager (solo para transports)
│
├── tools/            ← herramientas del agente
│   ├── registry.go   ← Tool struct + Registry (Register, Run, Definitions, Clone)
│   ├── bash.go       ← Bash tool
│   ├── file.go       ← ReadFile + WriteFile tools
│   ├── edit.go       ← Edit tool
│   ├── fetch.go      ← Fetch tool (text + binary via output_path)
│   └── skill.go      ← ReadSkill tool — importa agent/resources
│
├── resources/        ← contexto adicional para la sesión
│   ├── loader.go     ← ResourceLoader interface + Resources + SkillInfo
│   ├── file.go       ← FileResourceLoader (busca AGENTS.md, skills/ en cwd y padres)
│   └── nil.go        ← NilResourceLoader (tests, modo sin contexto)
│
└── store/            ← persistencia de mensajes
    ├── store.go      ← SessionStore + SessionStoreInstance interfaces
    ├── memory.go     ← InMemorySessionStore
    └── file.go       ← FsSessionStore (JSONL append-only)
```

### Dependency graph

```
agent/resources  → stdlib only
agent/store      → stdlib only
agent/tools      → agent/resources (ReadSkill necesita Resources + SkillInfo)
agent/           → agent/tools, agent/resources, agent/store, llm/, config/
```

Cero ciclos. Cada subdominio tiene una razón de existir clara.

---

## File Structure v2 — self-contained providers with shared `llm/` layer

After analysis: providers are self-contained in `providers/`, sharing a common `providers/llm/` subpackage for HTTP, types, and model knowledge.

```
providers/
├── provider.go       ← interface Provider (uses providers/llm/ types)
├── llm/              ← shared: types + HTTP + model knowledge
│   ├── types.go      ← Request, Response, StreamEvent, ToolCall, etc.
│   ├── sse.go        ← ParseSSE()
│   ├── image.go      ← ImageData, LoadImage
│   ├── openai.go     ← DoOpenAIStream, ParseOpenAIStream, format helpers
│   ├── anthropic.go  ← DoAnthropicStream, ParseAnthropicStream, buildThinking
│   └── registry.go   ← EnrichMeta, ApplyRegistryPricing, LookupModel, ModelMeta
├── anthropic.go      ← Anthropic provider (imports providers/llm/)
├── claude_oauth.go   ← ClaudeOAuth provider (imports providers/llm/)
├── openai.go         ← OpenAI provider (imports providers/llm/)
├── ollama.go         ← Ollama provider (imports providers/llm/)
├── ollama_cloud.go   ← OllamaCloud provider (imports providers/llm/)
├── opencode_go.go    ← OpenCodeGo provider (imports providers/llm/)
├── registry.go       ← All slice, Resolve, RefreshModels
└── status.go         ← GetProviderStatuses, GetModelGroups
```

### Key design decisions

- `providers/llm/` imports nothing from `providers/` — zero imports above itself
- `providers/llm/types.go` holds Request, Response, StreamEvent — the types Provider interface needs
- Providers in `providers/` import `providers/llm/` for HTTP + types
- Registry in `providers/` imports `providers/llm/` for Resolve
- Zero cyclic dependencies

### Why not subpackages per provider

Each provider as its own package creates a registry cycle:
```
providers/registry.go → providers/anthropic/ (for NewAnthropic)
providers/anthropic/ → providers/              (for Provider interface)
❌ CYCLE
```

Flat files in `providers/` avoid this — they share the `providers` package name.

### Dependency graph

```
providers/llm/      → stdlib, config/
providers/          → providers/llm/, config/
transport/          → providers/, config/
main                → transport/, providers/, config/
```

Not part of the Agent API. Used exclusively by transports (CLI, TUI) that need full session management UX.

Receives an `*Agent` as dependency:

```go
type SessionManager struct {
    agent *Agent
}

func NewSessionManager(a *Agent) *SessionManager

func (m *SessionManager) List(cwd string) ([]SessionMeta, error)
func (m *SessionManager) ListAll() ([]SessionMeta, error)
func (m *SessionManager) Delete(sessionID string) error
func (m *SessionManager) Rename(sessionID, name string) error
```

Transport flow:
```
transport → SessionManager → Agent.NewSession / Agent.ResumeSession → Session
```

SDK flow (no SessionManager needed):
```
agent := harness.New(opts)
session, _ := agent.NewSession(".")
session.Prompt(ctx, "hello")
```
