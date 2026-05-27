# Session Architecture — Design Document

> Diseño completo de la arquitectura de sesiones por capas para Harness.
> Inspirado en PI (mongo-sessions extension + JSONL store).
> Estado: **Diseño aprobado — pendiente implementación**.

---

## Motivación

El agent actual tiene un history `map[string][]json.RawMessage` en memoria con un
`maybeCompact` que tira mensajes viejos al azar. Sin persistencia, sin contexto adicional,
sin capacidad de reanudar sesiones. Necesitamos una arquitectura por capas.

---

## Stack de capas

```
┌─────────────────────────────────────┐
│           transport/                │  CLI, TUI, Telegram, etc.
│  (solo habla con SessionManager)    │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│         SessionManager              │  Crea, lista, carga, renombra sesiones
│  session/manager.go                 │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│            Session                  │  Una conversación activa
│  session/session.go                 │
│  ├── SessionStore  (persistencia)   │
│  └── ResourceLoader (contexto)      │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│             Agent                   │  ReAct loop (sin cambios)
│  agent/agent.go                     │
└──────────────┬──────────────────────┘
               │
┌──────────────▼──────────────────────┐
│          llm/providers              │  HTTP streaming
└─────────────────────────────────────┘
```

---

## Tipos de entrada JSONL (inspirado en PI)

```
session       → header de la sesión (id, cwd, timestamp, version)
session_info  → metadata mutable (nombre, tags)
model_change  → cambio de provider/model
thinking_change → cambio de thinking level
message       → mensaje user/assistant/tool (el más frecuente)
compaction    → summary de compactación + id del primer mensaje retenido
resource      → contexto cargado (AGENTS.md, skill, etc.)
```

Cada entry tiene: `id`, `parentId` (linked list), `timestamp`, `type` + campos propios.

---

## Interfaces

### SessionStore

```go
// session/store.go

// Entry es una línea JSONL — un evento atómico en la sesión.
type Entry struct {
    ID        string          `json:"id"`
    ParentID  string          `json:"parent_id,omitempty"`
    Timestamp time.Time       `json:"timestamp"`
    Type      string          `json:"type"`
    Data      json.RawMessage `json:"data,omitempty"`
}

// SessionStore persiste y lee entries de una sesión.
// Implementaciones: MemoryStore (default), FileStore (.jsonl), futuro: MongoStore
type SessionStore interface {
    // Append escribe un entry al store (append-only).
    Append(entry Entry) error
    // Entries devuelve todos los entries de la sesión.
    Entries() ([]Entry, error)
    // SessionID devuelve el ID único de la sesión.
    SessionID() string
    // Close cierra el store (flush, fsync, etc.).
    Close() error
}
```

### ResourceLoader

```go
// session/resources.go

// Resource es un bloque de contexto adicional inyectado al system prompt.
type Resource struct {
    ID      string // e.g. "agents-md", "skill-developer"
    Name    string // display name
    Content string // contenido completo
    Source  string // path o URL origen
}

// ResourceLoader descubre y carga contexto adicional para el agente.
type ResourceLoader interface {
    // Load devuelve todos los recursos disponibles para el cwd dado.
    Load(cwd string) ([]Resource, error)
}

// FileResourceLoader busca AGENTS.md, .harness/context.md, skills/ en el cwd y sus padres.
type FileResourceLoader struct {
    MaxDepth int // cuántos niveles subir buscando AGENTS.md
}
```

### Session

```go
// session/session.go

type SessionMeta struct {
    ID        string    `json:"id"`
    Name      string    `json:"name,omitempty"`    // nombre amigable, e.g. "agent-harness"
    CWD       string    `json:"cwd"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    Model     string    `json:"model"`             // último modelo usado
    Turns     int       `json:"turns"`             // cantidad de turnos completados
}

type Session struct {
    Meta    SessionMeta
    store   SessionStore
    loader  ResourceLoader
    agent   *agent.Agent
}

// NewSession crea una sesión nueva con store y loader dados.
func NewSession(provider llm.Provider, tools *tools.Registry, opts agent.Options,
    store SessionStore, loader ResourceLoader) (*Session, error)

// Chat ejecuta un turno: carga historial del store, llama al agent, persiste entries nuevos.
func (s *Session) Chat(ctx context.Context, text string, images []llm.ImageData) (string, error)

// Rename cambia el nombre amigable de la sesión.
func (s *Session) Rename(name string) error

// Compact ejecuta una compactación del historial (summary del modelo + trunca entries).
func (s *Session) Compact(ctx context.Context) error

// Close cierra la sesión y persiste el estado final.
func (s *Session) Close() error
```

### SessionManager

```go
// session/manager.go

type SessionManager struct {
    store   StoreFactory    // función que crea un SessionStore dado un ID
    loader  ResourceLoader
}

// StoreFactory crea un SessionStore para el ID y cwd dados.
type StoreFactory func(sessionID, cwd string) (SessionStore, error)

// New crea una sesión nueva con ID generado (UUID v7).
func (m *SessionManager) New(provider llm.Provider, tools *tools.Registry,
    opts agent.Options, cwd string) (*Session, error)

// Load carga una sesión existente por ID.
func (m *SessionManager) Load(sessionID string, provider llm.Provider,
    tools *tools.Registry, opts agent.Options) (*Session, error)

// List devuelve metadata de todas las sesiones conocidas por el store.
func (m *SessionManager) List() ([]SessionMeta, error)

// Delete elimina una sesión (soft delete).
func (m *SessionManager) Delete(sessionID string) error

// Rename renombra una sesión.
func (m *SessionManager) Rename(sessionID, name string) error
```

---

## Implementaciones de SessionStore

### MemoryStore (default, ya implementado parcialmente)

```go
// session/stores/memory.go
// Mantiene entries en un slice en memoria.
// Se pierde al terminar el proceso.
// Útil para tests y modo sin persistencia.

type MemoryStore struct {
    id      string
    entries []Entry
    mu      sync.RWMutex
}
```

### FileStore (.jsonl)

```go
// session/stores/file.go
// Cada sesión es un archivo .jsonl en ~/.harness/sessions/<cwd-encoded>/<id>.jsonl
// Cada línea = un Entry serializado.
// Append-only: nunca reescribe líneas existentes, solo agrega al final.
// Close() hace fsync.

type FileStore struct {
    id   string
    path string
    f    *os.File
    mu   sync.Mutex
}

// Directorio: ~/.harness/sessions/--Users-gustavo-Workspace--/<timestamp>_<id>.jsonl
// Mismo formato que PI para compatibilidad futura.
```

---

## ResourceLoader — cómo funciona

Cuando se crea una sesión nueva, el loader busca en el cwd y sube hasta `MaxDepth` niveles:

```
cwd/
├── AGENTS.md           ← cargado si existe (instrucciones del proyecto)
├── .harness/
│   └── context.md      ← contexto local del proyecto
└── skills/
    └── *.md            ← skills específicos del proyecto

~/.harness/
├── AGENTS.md           ← instrucciones globales del usuario
└── skills/
    └── *.md            ← skills globales
```

Los recursos se inyectan en el system prompt al inicio de la sesión y se registran
como entries `resource` en el store.

---

## Formato JSONL de FileStore

```jsonl
{"id":"019e4378","parent_id":null,"timestamp":"2026-05-26T...","type":"session","data":{"version":1,"cwd":"/Users/gustavo/Workspace/harness","model":"opencode-go/deepseek-v4-pro"}}
{"id":"a1b2c3d4","parent_id":"019e4378","timestamp":"2026-05-26T...","type":"resource","data":{"name":"AGENTS.md","source":"/Users/gustavo/Workspace/harness/AGENTS.md","size":10716}}
{"id":"b2c3d4e5","parent_id":"a1b2c3d4","timestamp":"2026-05-26T...","type":"model_change","data":{"provider":"opencode-go","model":"deepseek-v4-pro"}}
{"id":"c3d4e5f6","parent_id":"b2c3d4e5","timestamp":"2026-05-26T...","type":"message","data":{"role":"user","content":"dime del proyecto"}}
{"id":"d4e5f6g7","parent_id":"c3d4e5f6","timestamp":"2026-05-26T...","type":"message","data":{"role":"assistant","content":[...]}}
{"id":"e5f6g7h8","parent_id":"d4e5f6g7","timestamp":"2026-05-26T...","type":"compaction","data":{"summary":"...","first_kept_id":"c3d4e5f6","tokens_before":45000}}
```

---

## Cómo Session.Chat() funciona

```
Session.Chat(ctx, text, images)
    │
    ├── 1. Reconstruct LLM history from store entries
    │        entries → filter type=message → []json.RawMessage
    │        (solo los mensajes después del último compaction)
    │
    ├── 2. agent.Chat(ctx, sessionID, text, images)
    │        (el agent recibe el historial ya reconstruido)
    │
    ├── 3. On each agent.Event:
    │        EventTurnStart  → store.Append(message{role:user, content:text})
    │        EventToolCall   → store.Append(message{role:tool_call, ...})
    │        EventToolResult → store.Append(message{role:tool_result, ...})
    │        EventTurnEnd    → store.Append(message{role:assistant, content:...})
    │                          meta.UpdatedAt = now
    │                          meta.Turns++
    │
    ├── 4. maybeCompact() si history > threshold
    │
    └── 5. return response
```

---

## Compactación

```go
func (s *Session) Compact(ctx context.Context) error
```

1. Toma todos los mensajes actuales
2. Llama al LLM con prompt de resumen: `"Summarize this conversation..."`
3. Escribe entry `compaction` con `summary` + `first_kept_id` (último ~20 mensajes)
4. El historial reconstruido en futuros turnos: `[SystemPrompt + Summary] + últimos 20 mensajes`

Mismo enfoque que PI (`compaction` entry en JSONL).

---

## Cambios en el agent.go

El `Agent` necesita aceptar historial externo (de la Session):

```go
// Actual:
func (a *Agent) Chat(ctx context.Context, userID, text string, images []llm.ImageData) (string, error)

// Nuevo:
func (a *Agent) Chat(ctx context.Context, history []json.RawMessage, text string, images []llm.ImageData) (string, error)
// El historial viene de Session — el agent no lo gestiona más.
// Eliminar: a.history map, a.locks map, userLock(), ClearHistory(), maybeCompact()
```

El `Agent` se vuelve **stateless** — solo ejecuta un turno dado un historial externo.
El estado vive en `Session`. Mucho más limpio.

---

## Transport → SessionManager

```go
// Actual en transport/cli/cli.go:
t := cli.NewCLI(a, provider)
t.Run(ctx, a, provider)

// Nuevo:
sm := session.NewManager(
    session.WithFileStore("~/.harness/sessions"),
    session.WithFileResourceLoader(3),  // busca hasta 3 niveles arriba
)
s, _ := sm.New(provider, registry, agentOpts, cwd)
t := cli.NewCLI(s)  // transport solo habla con Session
t.Run(ctx)
```

### Comandos nuevos en transport:

```
/sessions          → lista sesiones (nombre, fecha, turns, modelo)
/session new       → nueva sesión
/session load <id> → carga sesión existente (continuar)
/session rename <name> → renombra la sesión activa
/session compact   → compacta el historial manualmente
```

---

## Estructura de archivos nueva

```
session/                          ← nuevo package
├── session.go                    ← Session struct + Chat() + Compact()
├── manager.go                    ← SessionManager
├── store.go                      ← SessionStore interface + Entry type
├── resources.go                  ← ResourceLoader interface + Resource type
└── stores/
    ├── memory.go                 ← MemoryStore
    └── file.go                   ← FileStore (.jsonl)
```

```
~/.harness/
├── credentials.json
├── settings.json
└── sessions/
    ├── --Users-gustavo-Workspace-harness--/
    │   ├── 2026-05-26T10-00-00_<id>.jsonl
    │   └── 2026-05-26T14-30-00_<id>.jsonl
    └── --Users-gustavo--/
        └── 2026-05-26T09-00-00_<id>.jsonl
```

---

## Dependency graph final

```
config/    → stdlib only
llm/       → config/
agent/     → llm/, config/          ← se vuelve stateless
session/   → agent/, llm/, config/  ← nuevo, gestiona estado
transport/ → session/, llm/, config/
main       → session/, transport/, config/
```

---

## Plan de implementación

### Fase 1: Interfaces y MemoryStore
- Definir `Entry`, `SessionStore`, `ResourceLoader` interfaces
- Implementar `MemoryStore`
- Refactorizar `Agent.Chat()` para recibir historial externo (stateless)
- `Session` con `MemoryStore` — feature parity con estado actual

### Fase 2: FileStore
- Implementar `FileStore` con append-only JSONL
- `SessionManager.List()` / `New()` / `Load()`
- Comandos `/sessions`, `/session load`, `/session new`
- Persistencia automática

### Fase 3: ResourceLoader
- `FileResourceLoader` — busca `AGENTS.md`, `.harness/context.md`, `skills/`
- Inyección en system prompt al crear sesión
- Entry `resource` en el store

### Fase 4: Compactación real
- `Session.Compact()` — summary via LLM
- `maybeCompact()` automático en Session
- Entry `compaction` en JSONL

---

## Referencias

- PI session JSONL: `~/.pi/agent/sessions/**/*.jsonl`
- PI mongo-sessions extension: `/Users/gustavo/.pi/agent/extensions/mongo-sessions/index.ts`
- Entry types observados en PI: `session`, `session_info`, `model_change`, `thinking_level_change`, `message`, `compaction`
- Chunk strategy de PI: 200 entries/chunk, append incremental, full-resync en compaction
