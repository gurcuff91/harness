# Memory store port — design

**Status:** approved (2026-10-04) · **Breaking:** yes (SDK)

## Goal

Make persistent memory pluggable per agent, the same way `SessionStore`
(`AgentWithStore`) and `ResourceLoader` (`AgentWithResourceLoader`) already
are. Memory is the last piece of state without a port.

## Why per-agent, not process-global

`configstore` is process-global because its state is (providers, credentials,
instances, schedules). Memory is consumed exclusively through `Agent`: the
agent opens it, closes it, and hands it to its subagents. Per-agent stores
also enable multi-tenant embedding (two agents, two memories, one process)
and trivially isolated tests.

## Design

### Port — `agent/memory`

```go
type Store interface {
    Write(cwd, slug, content string, global bool) (created bool, err error)
    Search(cwd, query string, includeContent bool, skip, limit int) (SearchResult, error)
    Delete(cwd, slug string, global bool) (bool, error)
    Close() error
}
```

`Search`'s cwd contract is documented on the interface so any backend honors
it: `""` = all projects, `GlobalCWD` = global memories only, a path = that
project plus globals. `global=true` on Write/Delete maps to `GlobalCWD`.

### Default implementation

- The current struct becomes `SQLiteStore`; `Open(path)` becomes
  `OpenSQLite(path) (*SQLiteStore, error)` (`""` = `~/.harness/agent/memory.db`).
- `Get` stays on `SQLiteStore` only (no external caller).
- `ToolAdapter` wraps the `Store` interface.

### Agent wiring

- `AgentOptions.EnableMemory bool` is removed; `AgentOptions.Memory
  memory.Store` replaces it. **Presence of a store = memory on; nil = off.**
  No more two-knob combination.
- `agent.New` never opens a database itself.
- `Agent.Close()` closes the injected store (same ownership as `Store`):
  closing the agent means the program is done.
- Subagents receive the parent's store through the unexported
  `sharedMemory` field (now interface-typed) and never close it.
- `Agent.Memory()` returns `memory.Store`; `server`'s `/api/memories` and the
  system prompt's slug listing keep working unchanged.

### Facade

`harness.AgentWithMemory()` → `harness.AgentWithMemory(s memory.Store)`. No
`AgentWithSQLiteMemory` shortcut — opening is explicit and the caller owns the
error:

```go
mem, err := memory.OpenSQLite("")
if err != nil { /* caller decides */ }
a := harness.NewAgent(harness.AgentWithMemory(mem))
```

### CLI

`internal/cli/agent.go` gets a `defaultMemory() memory.Store` helper used by
`newAgent`, `newOneShotAgent` and `newInteractiveAgent`. On open failure it
returns an **untyped nil interface** (never a typed-nil `*SQLiteStore`, which
would make `a.memStore != nil` true and break the first Search) — preserving
today's degrade-silently behavior. Covered by a test. Transports need no
change: they all build agents through these constructors.

## Touch list

`agent/memory/{store,adapter,store_test}.go`, `agent/agent.go`,
`agent/prompts.go` (comment), `harness.go`, `harness_test.go`,
`internal/cli/agent.go`, `README.md`, `AGENTS.md`, `CHANGELOG.md`.

## Testing

- Existing memory store tests on `OpenSQLite`.
- Agent: nil Memory → no Memo* tools, no `## Memory` prompt block; injected
  fake store → tools registered, `Close()` closes it once; subagent shares
  and does not close.
- `defaultMemory()` failure path returns an untyped nil.
- Facade: `AgentWithMemory(s)` sets `o.Memory`.
