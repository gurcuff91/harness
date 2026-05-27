# Session Architecture — Detailed Design

> Diseño detallado por componente, de abajo hacia arriba.
> Inspirado en PI (`AgentHarness` + `SessionManager` + `ResourceLoader`).
> Objetivo dual: CLI tool + Go SDK embebible.

---

## Principio de diseño: dual-use

PI lo hace en JS. Lo haremos en Go.

```
# Como CLI (lo que existe hoy):
harness

# Como SDK (nuevo — go get):
import "github.com/gurcuff91/harness/sdk"

h := sdk.New(sdk.Options{
    Model:    "anthropic/claude-sonnet-4-20250514",
    APIKey:   os.Getenv("ANTHROPIC_API_KEY"),
    CWD:      ".",
})
resp, _ := h.Prompt(ctx, "What files are in this directory?")
fmt.Println(resp.Text)
```

---

## Stack completo de capas

```
┌──────────────────────────────────────────────────┐
│           transport/  (CLI, TUI, future: Web)    │
│  habla únicamente con Session                    │
└────────────────────┬─────────────────────────────┘
                     │
┌────────────────────▼─────────────────────────────┐
│              session.Session                      │
│  Una conversación activa. Orquesta todo.          │
│  ├── SessionStore    (persistencia de entries)   │
│  └── ResourceLoader  (AGENTS.md, skills, ctx)    │
└────────────────────┬─────────────────────────────┘
                     │
┌────────────────────▼─────────────────────────────┐
│              agent.Agent                         │
│  STATELESS. Solo ejecuta un turno.               │
│  Recibe historial externo. No persiste nada.     │
└────────────────────┬─────────────────────────────┘
                     │
┌────────────────────▼─────────────────────────────┐
│           llm/providers                          │
│  HTTP streaming. Sin cambios.                    │
└──────────────────────────────────────────────────┘

SDK público:
┌──────────────────────────────────────────────────┐
│              sdk.Harness                         │
│  Wrapper fino sobre Session para uso embebido.   │
│  API simple: New() + Prompt() + Subscribe()      │
└──────────────────────────────────────────────────┘
```

---

## Capa 1: `agent.Agent` — STATELESS (refactor)

### Cambio central

El Agent deja de tener memoria interna. Recibe historial del caller.
Se vuelve un ejecutor puro de un turno LLM.

```go
// ANTES (con estado):
func (a *Agent) Chat(ctx context.Context, userID, text string, images []llm.ImageData) (string, error)
// guarda history internamente, tiene locks por userID

// DESPUÉS (stateless):
func (a *Agent) RunTurn(ctx context.Context, req TurnRequest) (*TurnResult, error)
```

### TurnRequest / TurnResult

```go
// agent/agent.go

// TurnRequest es todo lo que el agente necesita para ejecutar UN turno.
type TurnRequest struct {
    // Historial completo de la conversación hasta ahora.
    // El caller (Session) es responsable de reconstruirlo.
    History []json.RawMessage

    // El mensaje del usuario para este turno.
    UserText   string
    UserImages []llm.ImageData

    // System prompt ya construido (incluye recursos inyectados).
    SystemPrompt string

    // Límites
    MaxTokens int
    MaxLoops  int
}

// TurnResult es el output completo de un turno.
type TurnResult struct {
    // Respuesta final del modelo (texto).
    Text string

    // El mensaje assistant completo para agregar al historial.
    AssistantMessage json.RawMessage

    // Tool calls que ocurrieron durante el turno (para el store).
    ToolCalls   []ToolCallRecord
    ToolResults []ToolResultRecord

    // Uso de tokens acumulado del turno.
    Usage llm.Usage
}

type ToolCallRecord struct {
    ID   string
    Name string
    Args json.RawMessage
}

type ToolResultRecord struct {
    CallID string
    Name   string
    Output string
    IsErr  bool
}
```

### Métodos del Agent

```go
type Agent struct {
    provider llm.Provider
    tools    *tools.Registry
    opts     Options
    onEvent  Handler
}

// RunTurn ejecuta un turno completo del ReAct loop.
// Es stateless — no guarda nada internamente.
func (a *Agent) RunTurn(ctx context.Context, req TurnRequest) (*TurnResult, error)

// OnEvent registra handler para eventos del transport layer.
func (a *Agent) OnEvent(h Handler)

// Provider / SetProvider para cambio de modelo en runtime.
func (a *Agent) Provider() llm.Provider
func (a *Agent) SetProvider(p llm.Provider)

// SetThinkingLevel propaga al provider.
func (a *Agent) SetThinkingLevel(level string)
```

**Lo que se elimina del Agent:**
- `history map[string][]json.RawMessage` ← va a Session.store
- `locks map[string]*sync.Mutex` ← no necesario (Session serializa turnos)
- `userLock()` ← eliminado
- `ClearHistory()` ← Session maneja esto
- `maybeCompact()` ← Session maneja compactación real

---

## Capa 2: `session/store.go` — SessionStorage interface

Inspirado en `SessionStorage<TMetadata>` de PI.

```go
// session/store.go

// Entry es un evento atómico en la sesión — una línea JSONL.
type Entry struct {
    ID        string          `json:"id"`
    ParentID  string          `json:"parent_id,omitempty"` // null para el header
    Timestamp time.Time       `json:"timestamp"`
    Type      string          `json:"type"`

    // Payload según Type (embedded, no wrapper)
    // Para "message": los campos del mensaje
    // Para "compaction": summary, firstKeptEntryId, etc.
    // etc.
}

// Los tipos de entry concretos
type SessionHeader struct {
    Type      string `json:"type"`  // "session"
    Version   int    `json:"version"`
    ID        string `json:"id"`
    Timestamp string `json:"timestamp"`
    CWD       string `json:"cwd"`
    Model     string `json:"model"`
}

type MessageEntry struct {
    Entry
    Role    string          `json:"role"`    // "user" | "assistant" | "tool"
    Content json.RawMessage `json:"content"` // formato del provider
}

type ModelChangeEntry struct {
    Entry
    Provider string `json:"provider"`
    ModelID  string `json:"model_id"`
}

type ThinkingChangeEntry struct {
    Entry
    Level string `json:"thinking_level"`
}

type ResourceEntry struct {
    Entry
    Name    string `json:"name"`
    Source  string `json:"source"`
    Size    int    `json:"size"`
}

type CompactionEntry struct {
    Entry
    Summary         string `json:"summary"`
    FirstKeptID     string `json:"first_kept_entry_id"`
    TokensBefore    int    `json:"tokens_before"`
}

type SessionInfoEntry struct {
    Entry
    Name string `json:"name,omitempty"`
}

// SessionStore es la interfaz de persistencia.
// Implementaciones: MemoryStore, FileStore (jsonl), futuro: MongoStore
type SessionStore interface {
    // ID devuelve el ID único de la sesión.
    ID() string

    // Append escribe un entry (append-only, nunca modifica entries existentes).
    Append(entry any) error

    // Entries devuelve todos los entries desde un ID dado.
    // Si fromID es "", devuelve todos.
    Entries(fromID string) ([]json.RawMessage, error)

    // LeafID devuelve el ID del entry más reciente en la rama activa.
    LeafID() string

    // Close cierra el store (flush/fsync).
    Close() error
}
```

### Implementaciones

```go
// session/stores/memory.go
type MemoryStore struct {
    id      string
    entries []json.RawMessage
    leafID  string
    mu      sync.RWMutex
}

// session/stores/file.go
// ~/.harness/sessions/<encoded-cwd>/<timestamp>_<uuid>.jsonl
// append-only: os.OpenFile con O_APPEND|O_CREATE|O_WRONLY
// cada línea = json.Marshal(entry) + "\n"
type FileStore struct {
    id     string
    path   string
    f      *os.File
    mu     sync.Mutex
    leafID string
}
```

---

## Capa 3: `session/resources.go` — ResourceLoader

Inspirado en `ResourceLoader` de PI, simplificado para Go.

```go
// session/resources.go

// Resource es un bloque de contexto adicional inyectado al system prompt.
type Resource struct {
    ID      string // "agents-md", "skill-developer", "local-context"
    Name    string // display name para el modelo
    Content string // contenido completo del archivo
    Source  string // path absoluto origen
}

// ResourceLoader descubre y carga contexto adicional para la sesión.
type ResourceLoader interface {
    // Load devuelve los recursos disponibles para el cwd dado.
    // Se llama una vez al crear la sesión.
    Load(cwd string) ([]Resource, error)
}

// FileResourceLoader busca archivos de contexto en el cwd y padres.
// Orden de búsqueda:
//   1. <cwd>/AGENTS.md
//   2. <cwd>/.harness/context.md
//   3. <parent>/AGENTS.md (sube hasta MaxDepth niveles)
//   4. ~/.harness/AGENTS.md (global)
//   5. ~/.harness/skills/*.md
type FileResourceLoader struct {
    MaxDepth int // default: 3
}

func (r *FileResourceLoader) Load(cwd string) ([]Resource, error)

// NilResourceLoader — no carga nada. Útil para tests y modo sin contexto.
type NilResourceLoader struct{}
```

### Cómo se inyectan los recursos

Al crear una `Session`, los recursos se inyectan en el system prompt:

```go
func buildSystemPrompt(base string, resources []Resource) string {
    if len(resources) == 0 {
        return base
    }
    var sb strings.Builder
    sb.WriteString(base)
    sb.WriteString("\n\n---\n\n")
    for _, r := range resources {
        fmt.Fprintf(&sb, "## %s\n\n%s\n\n", r.Name, r.Content)
    }
    return sb.String()
}
```

Y se registran como entries en el store:
```go
for _, r := range resources {
    store.Append(ResourceEntry{
        Entry: newEntry(leafID, "resource"),
        Name: r.Name, Source: r.Source, Size: len(r.Content),
    })
}
```

---

## Capa 4: `session/session.go` — Session

La pieza central. Orquesta agent, store y loader.

```go
// session/session.go

type SessionMeta struct {
    ID        string
    Name      string    // nombre amigable (opcional)
    CWD       string
    Model     string    // último modelo usado
    CreatedAt time.Time
    UpdatedAt time.Time
    Turns     int
}

type Session struct {
    meta      SessionMeta
    store     SessionStore
    resources []Resource      // cargados al inicio, inmutables
    agent     *agent.Agent
    provider  llm.Provider
    tools     *tools.Registry
    opts      agent.Options
    systemPrompt string       // construido = base + resources
    mu        sync.Mutex      // serializa turnos (un turno a la vez)
}
```

### Constructor

```go
func NewSession(
    provider llm.Provider,
    tools *tools.Registry,
    opts agent.Options,
    store SessionStore,
    loader ResourceLoader,
    cwd string,
) (*Session, error) {
    // 1. Cargar recursos
    resources, _ := loader.Load(cwd)

    // 2. Construir system prompt
    systemPrompt := buildSystemPrompt(opts.SystemPrompt, resources)

    // 3. Registrar entry "session" en el store
    store.Append(SessionHeader{...})

    // 4. Registrar entries "resource" por cada recurso cargado
    for _, r := range resources {
        store.Append(ResourceEntry{...})
    }

    // 5. Registrar "model_change" inicial
    store.Append(ModelChangeEntry{...})

    s := &Session{
        meta: SessionMeta{
            ID:        store.ID(),
            CWD:       cwd,
            Model:     provider.Model(),
            CreatedAt: time.Now(),
        },
        store: store, resources: resources, agent: agent.New(provider, tools, opts),
        provider: provider, tools: tools, opts: opts, systemPrompt: systemPrompt,
    }
    return s, nil
}
```

### Método principal: Chat

```go
// Chat ejecuta un turno completo y lo persiste.
func (s *Session) Chat(ctx context.Context, text string, images []llm.ImageData) (string, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // 1. Reconstruir historial LLM desde el store
    history, err := s.buildHistory()
    if err != nil {
        return "", err
    }

    // 2. Persistir el mensaje del usuario ANTES de llamar al agente
    userEntry := s.store.Append(MessageEntry{
        Role: "user", Content: formatUserMessage(text, images),
    })

    // 3. Ejecutar el turno (agent es stateless)
    result, err := s.agent.RunTurn(ctx, agent.TurnRequest{
        History:      history,
        UserText:     text,
        UserImages:   images,
        SystemPrompt: s.systemPrompt,
        MaxTokens:    s.opts.MaxTokens,
        MaxLoops:     s.opts.MaxLoops,
    })

    // 4. Persistir tool calls + results
    for i, tc := range result.ToolCalls {
        s.store.Append(ToolCallEntry{...})
        s.store.Append(ToolResultEntry{...result.ToolResults[i]})
    }

    // 5. Persistir respuesta del asistente
    s.store.Append(MessageEntry{
        Role: "assistant", Content: result.AssistantMessage,
    })

    // 6. Actualizar metadata
    s.meta.Turns++
    s.meta.UpdatedAt = time.Now()
    s.meta.Model = s.provider.Model()

    // 7. Auto-compact si necesario
    s.maybeCompact(ctx)

    return result.Text, err
}
```

### Reconstrucción del historial

```go
// buildHistory reconstruye el []json.RawMessage que el agent/LLM recibe.
// Solo incluye mensajes desde el último compaction (si existe).
func (s *Session) buildHistory() ([]json.RawMessage, error) {
    entries, _ := s.store.Entries("")
    // Encontrar último compaction
    lastCompaction := findLastCompaction(entries)
    if lastCompaction != nil {
        // Prepend compaction summary como mensaje user + assistant
        return buildFromCompaction(lastCompaction, entries), nil
    }
    // Filtrar solo entries type=message y convertir a json.RawMessage
    return filterMessages(entries), nil
}
```

### Otros métodos de Session

```go
// Rename cambia el nombre amigable de la sesión.
func (s *Session) Rename(name string) error {
    s.store.Append(SessionInfoEntry{Entry: newEntry(..., "session_info"), Name: name})
    s.meta.Name = name
    return nil
}

// Compact ejecuta compactación manual (summary via LLM + trunca historial).
func (s *Session) Compact(ctx context.Context, instructions string) error

// SwitchModel cambia el modelo en runtime y lo persiste.
func (s *Session) SwitchModel(provider llm.Provider) error {
    s.provider = provider
    s.agent.SetProvider(provider)
    s.meta.Model = provider.Model()
    s.store.Append(ModelChangeEntry{Provider: ..., ModelID: ...})
    return nil
}

// SwitchThinking cambia el nivel de thinking y lo persiste.
func (s *Session) SwitchThinking(level string) error {
    s.agent.SetThinkingLevel(level)
    s.store.Append(ThinkingChangeEntry{Level: level})
    return nil
}

// Meta devuelve la metadata de la sesión (read-only copy).
func (s *Session) Meta() SessionMeta

// Close cierra la sesión y el store.
func (s *Session) Close() error { return s.store.Close() }

// OnEvent delega al agent para que el transport reciba eventos.
func (s *Session) OnEvent(h agent.Handler) { s.agent.OnEvent(h) }
```

---

## Capa 5: `session/manager.go` — SessionManager

```go
// session/manager.go

type ManagerOptions struct {
    // Directorio raíz de sesiones. Default: ~/.harness/sessions/
    SessionsDir string
    // Loader para AGENTS.md, skills, etc.
    Loader ResourceLoader
    // Si true, no persiste nada (útil para tests).
    InMemory bool
}

type Manager struct {
    opts ManagerOptions
}

func NewManager(opts ManagerOptions) *Manager

// New crea una sesión nueva para el cwd actual.
func (m *Manager) New(
    provider llm.Provider,
    tools *tools.Registry,
    opts agent.Options,
    cwd string,
) (*Session, error) {
    var store SessionStore
    if m.opts.InMemory {
        store = stores.NewMemoryStore(newUUID())
    } else {
        store = stores.NewFileStore(m.sessionPath(cwd), newUUID())
    }
    return NewSession(provider, tools, opts, store, m.opts.Loader, cwd)
}

// Load carga una sesión existente por ID desde el FS.
func (m *Manager) Load(
    sessionID string,
    provider llm.Provider,
    tools *tools.Registry,
    opts agent.Options,
) (*Session, error)

// Resume carga la sesión más reciente para el cwd dado, o crea una nueva.
func (m *Manager) Resume(
    provider llm.Provider,
    tools *tools.Registry,
    opts agent.Options,
    cwd string,
) (*Session, error)

// List devuelve metadata de todas las sesiones conocidas.
func (m *Manager) List(cwd string) ([]SessionMeta, error)
func (m *Manager) ListAll() ([]SessionMeta, error)

// Delete elimina una sesión (mueve el archivo a trash o borra).
func (m *Manager) Delete(sessionID string) error

// sessionPath genera el path del archivo JSONL para un cwd dado.
// ~/.harness/sessions/--Users-gustavo-Workspace-harness--/<ts>_<id>.jsonl
func (m *Manager) sessionPath(cwd string) string
```

---

## Capa 6: `sdk/harness.go` — SDK público (dual-use)

Inspirado en `AgentHarness` de PI. API simple para embedders.

```go
// sdk/harness.go
// Package sdk provides a simple programmatic interface for embedding harness.
//
// Quick start:
//
//   h, _ := sdk.New(sdk.Options{
//       Model:    "anthropic/claude-sonnet-4",
//       APIKey:   os.Getenv("ANTHROPIC_API_KEY"),
//   })
//   defer h.Close()
//
//   resp, _ := h.Prompt(ctx, "List files in this directory")
//   fmt.Println(resp.Text)

package sdk

type Options struct {
    // Model en formato "provider/model". Requerido.
    Model string

    // API key para el provider. Si vacío, usa env vars.
    APIKey string

    // Directorio de trabajo. Default: cwd del proceso.
    CWD string

    // System prompt custom. Si vacío, usa el default.
    SystemPrompt string

    // Si true, no persiste sesión en disco (InMemory).
    InMemory bool

    // Directorio de sesiones. Default: ~/.harness/sessions/
    SessionsDir string

    // SessionID para reanudar sesión existente. Si vacío, crea nueva.
    ResumeSessionID string

    // Tools a registrar. Si nil, registra las tools por defecto.
    Tools []tools.Tool

    // ResourceLoader custom. Si nil, usa FileResourceLoader.
    Loader session.ResourceLoader

    // Handler de eventos del agente.
    OnEvent agent.Handler

    // Thinking level. Default: "high".
    ThinkingLevel string

    // Max iteraciones del ReAct loop. Default: 25.
    MaxLoops int

    // Max tokens por respuesta. Default: 8192.
    MaxTokens int
}

type PromptResult struct {
    Text     string
    Thinking string
    Usage    llm.Usage
}

// Harness es el objeto principal del SDK.
// Thread-safe: serializa llamadas a Prompt internamente.
type Harness struct {
    session *session.Session
    manager *session.Manager
}

// New crea un nuevo Harness con la configuración dada.
func New(opts Options) (*Harness, error)

// Prompt envía un mensaje y espera la respuesta completa.
func (h *Harness) Prompt(ctx context.Context, text string) (*PromptResult, error)

// PromptWithImages envía un mensaje con imágenes.
func (h *Harness) PromptWithImages(ctx context.Context, text string, images []llm.ImageData) (*PromptResult, error)

// SwitchModel cambia el modelo en runtime.
func (h *Harness) SwitchModel(fullModel string) error

// SwitchThinking cambia el nivel de thinking.
func (h *Harness) SwitchThinking(level string) error

// ClearHistory limpia el historial (crea nueva sesión en memoria).
func (h *Harness) ClearHistory() error

// Session devuelve la sesión activa (para acceso avanzado).
func (h *Harness) Session() *session.Session

// SessionMeta devuelve la metadata de la sesión activa.
func (h *Harness) SessionMeta() session.SessionMeta

// Close cierra la sesión y persiste el estado final.
func (h *Harness) Close() error
```

### Uso del SDK

```go
package main

import (
    "context"
    "fmt"
    "github.com/gurcuff91/harness/sdk"
)

func main() {
    h, err := sdk.New(sdk.Options{
        Model:  "anthropic/claude-sonnet-4-20250514",
        APIKey: os.Getenv("ANTHROPIC_API_KEY"),
        CWD:    ".",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer h.Close()

    // Streaming events (opcional)
    h.Session().OnEvent(func(e agent.Event) {
        if e.Type == agent.EventStreamTextDelta {
            fmt.Print(e.Delta)
        }
    })

    resp, _ := h.Prompt(context.Background(), "What's in this directory?")
    fmt.Println(resp.Text)
}
```

---

## Estructura de archivos

```
session/                         ← nuevo package
├── session.go                   ← Session struct + Chat() + Compact()
├── manager.go                   ← Manager: New/Load/Resume/List
├── store.go                     ← SessionStore interface + Entry types
├── resources.go                 ← ResourceLoader interface + FileResourceLoader
├── history.go                   ← buildHistory(), buildSystemPrompt()
├── compaction.go                ← Compact(), maybeCompact(), buildCompactionPrompt()
└── stores/
    ├── memory.go                ← MemoryStore (tests + in-memory mode)
    └── file.go                  ← FileStore (JSONL append-only)

sdk/                             ← nuevo package (SDK público)
└── harness.go                   ← Harness + Options + PromptResult

agent/                           ← refactor
├── agent.go                     ← Agent stateless + RunTurn()
├── event.go                     ← sin cambios
└── tools/                       ← sin cambios

~/.harness/
├── credentials.json
├── settings.json
└── sessions/
    ├── --Users-gustavo-Workspace-harness--/
    │   └── 2026-05-26T10-00-00_<uuid>.jsonl
    └── --Users-gustavo--/
        └── 2026-05-26T09-00-00_<uuid>.jsonl
```

---

## Dependency graph final

```
stdlib
  ↑
config/        → stdlib
  ↑
llm/           → config/
  ↑
agent/         → llm/, config/          (STATELESS, refactored)
  ↑
session/       → agent/, llm/, config/  (nuevo, gestiona estado)
  ↑
sdk/           → session/, agent/, llm/ (nuevo, API pública)
  ↑
transport/     → session/, agent/, llm/, config/
  ↑
main           → session/, transport/, config/
```

---

## Comandos nuevos en transport/cli

```
/sessions           → lista sesiones del cwd actual
/session new        → nueva sesión (olvida contexto actual)
/session resume     → muestra lista para reanudar
/session load <id>  → carga sesión por ID
/session rename <n> → renombra la sesión activa
/session compact    → compacta el historial manualmente
/session info       → stats: turns, tokens, costo, archivo
```

---

## Plan de implementación (de abajo hacia arriba)

### Fase 1: Agent stateless
- Refactorizar `agent.Agent.Chat()` → `RunTurn(TurnRequest) *TurnResult`
- Eliminar `history`, `locks`, `maybeCompact` del agent
- Tests unitarios del agent con historial mockeado

### Fase 2: SessionStore
- `Entry` types + marshaling JSONL
- `MemoryStore` (tests)
- `FileStore` (JSONL append-only, ~/.harness/sessions/)

### Fase 3: ResourceLoader
- `FileResourceLoader` (AGENTS.md, .harness/context.md, skills/)
- `NilResourceLoader` (tests)

### Fase 4: Session
- `NewSession()` + `Chat()` + `buildHistory()` + `SwitchModel()` + `Rename()`
- `buildSystemPrompt()` con recursos inyectados
- Integrar con transport/cli (reemplazar uso directo de agent)

### Fase 5: Manager + comandos CLI
- `Manager.New/Load/Resume/List`
- Comandos `/sessions`, `/session new`, `/session load`, etc.

### Fase 6: Compaction real
- `Session.Compact()` — summary via LLM
- `maybeCompact()` automático (threshold configurable)
- Entry `compaction` en JSONL

### Fase 7: SDK
- `sdk.Harness` + `sdk.Options` + `sdk.New()`
- Ejemplos en `examples/`
- Documentación en `docs/sdk.md`

---

## Formato JSONL de FileStore (compatibilidad con PI)

```jsonl
{"type":"session","version":1,"id":"019e4378","timestamp":"2026-05-26T10:00:00Z","cwd":"/Users/gustavo/Workspace/harness","model":"opencode-go/deepseek-v4-pro"}
{"id":"a1b2c3d4","parent_id":"019e4378","timestamp":"2026-05-26T10:00:00Z","type":"resource","name":"AGENTS.md","source":"/Users/gustavo/Workspace/harness/AGENTS.md","size":10716}
{"id":"b2c3d4e5","parent_id":"a1b2c3d4","timestamp":"2026-05-26T10:00:00Z","type":"model_change","provider":"opencode-go","model_id":"deepseek-v4-pro"}
{"id":"c3d4e5f6","parent_id":"b2c3d4e5","timestamp":"2026-05-26T10:00:01Z","type":"message","role":"user","content":{"type":"text","text":"dime del proyecto"}}
{"id":"d4e5f6g7","parent_id":"c3d4e5f6","timestamp":"2026-05-26T10:00:03Z","type":"message","role":"tool_call","name":"bash","args":{"command":"ls"},"call_id":"tc_abc"}
{"id":"e5f6g7h8","parent_id":"d4e5f6g7","timestamp":"2026-05-26T10:00:03Z","type":"message","role":"tool_result","call_id":"tc_abc","output":"harness main.go go.mod","is_error":false}
{"id":"f6g7h8i9","parent_id":"e5f6g7h8","timestamp":"2026-05-26T10:00:05Z","type":"message","role":"assistant","content":[{"type":"text","text":"El proyecto es..."}]}
{"id":"g7h8i9j0","parent_id":"f6g7h8i9","timestamp":"2026-05-26T10:00:05Z","type":"compaction","summary":"...resumen...","first_kept_entry_id":"c3d4e5f6","tokens_before":45000}
```
