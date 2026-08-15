# Built-in tools ignore Session.CWD — Design

**Date:** 2026-08-14
**Status:** Approved, implementing
**Area:** `agent/tools/{bash,file,edit}.go`, `agent/agent.go` (system prompt + tool registration)

## Problem

Reported by a third-party dev embedding harness as a library (`jade-kaiban serve`,
one long-running process hosting multiple concurrent sessions, each with its own
real working directory).

`Bash`, `ReadFile`, `WriteFile`, `Edit` (`agent/tools/{bash,file,edit}.go`) never
apply `Session.CWD` — confirmed by inspection: zero occurrences of `cmd.Dir =`,
`os.Chdir`, or `filepath.Join(cwd, ...)` in those files. They operate against the
OS process's real working directory, not the session's logical one. Contrast:
`MemoWrite`/`MemoSearch`/`MemoDelete` (`agent/tools/memory.go`) DO receive `cwd`
explicitly in their constructor and partition by it — the pattern already exists
in the codebase, it just was never applied to the file/exec tools.

`Session.CWD` today only reaches `buildSystemPrompt` (printed as
`## Working Directory\n\n<cwd>`, purely informational) — never applied by code to
an actual filesystem/process operation.

## Why it was invisible until now

Every official harness transport (CLI, TUI, Telegram, Slack, ACP, `server`) calls
`os.Getwd()` exactly ONCE per process at startup and treats that as "the" cwd —
implicitly assuming one process runs one active session's worth of cwd at a time
(the TUI's actual usage pattern). In that world, `Session.CWD == os.Getwd()`
always, so the missing enforcement is unobservable: relative paths "happen" to
resolve correctly because there's only one cwd in play.

The third-party report is the first real consumer exercising
`NewSession(cwd, model)`'s actual promise — many concurrent sessions, each with a
DIFFERENT real cwd, in one process (e.g. one workspace folder per
product/version). There, all sessions silently share the process's real cwd for
Bash/Read/Write/Edit, breaking the per-session isolation the API implies.

Not a documented/deliberate 1-process-1-session assumption — just a gap nobody
exercised.

## Performance impact

None measurable. `cmd.Dir = cwd` is a struct field assignment; `filepath.Join`
guarded by `filepath.IsAbs` is a cheap string check + concatenation, invoked once
per tool call. Negligible next to actual I/O or LLM round-trip latency.

## Fix — two layers (approach B)

### Layer 1 — code enforcement (the actual isolation fix)

**Constructors take `cwd string`**, same pattern as `MemoWrite(store, cwd)`:
```go
func Bash(cwd string) Tool
func ReadFile(cwd string) Tool
func WriteFile(cwd string) Tool
func Edit(cwd string) Tool
```
Sole caller: `agent.go`'s `buildSessionTools` (already has `cwd` in scope) — no
external SDK consumers call these constructors directly, so no breaking change
for anyone but harness itself.

**Path resolution** (`file.go`, `edit.go`) — a shared helper:
```go
func resolvePath(cwd, path string) string {
    if filepath.IsAbs(path) {
        return path // respected as-is — no sandboxing, same trust model as today
    }
    return filepath.Join(cwd, path)
}
```
Applied before every `os.ReadFile`/`os.WriteFile` call. An absolute path outside
the session's cwd is still allowed, unchanged from today's behavior — this fix is
about correct RELATIVE resolution, not introducing a sandbox (that would be a
separate, larger design decision, and isn't what was reported).

**Bash** (`bash.go`) — `cmd.Dir = cwd` on BOTH `exec.Command` call sites: the
foreground path and `runBashBackground`. Without covering both, background
commands would remain the broken case even after fixing the foreground one.

### Layer 2 — system prompt reinforcement

Today's `## Working Directory\n\n<cwd>` is purely informational; nothing in the
prompt instructs the model to prefer absolute paths — the model does so today
only by learned convention (e.g. from Claude Code-style training), not because
harness asks it to. That's an unenforced assumption that happens to hold in the
single-cwd-per-process world and isn't guaranteed. Add an explicit instruction
alongside the existing cwd line:

> "Prefer absolute paths when reading, writing, or editing files, and when
> running Bash commands that touch specific files. Relative paths resolve
> against this working directory, but an absolute path is unambiguous."

This doesn't replace the code fix — it's a second, complementary layer: code
corrects whatever arrives malformed; the prompt reduces how often that happens.

## Testing

- `Bash`/`ReadFile`/`WriteFile`/`Edit` built with a `cwd` from `t.TempDir()`
  DIFFERENT from the real process cwd; a relative path resolves inside that
  temp dir (create/read a file; for Bash, run `pwd` and compare output).
- Absolute paths still work unchanged regardless of the `cwd` passed in (no
  regression on today's behavior).
- Explicit regression: two tool sets built with two different `cwd`s (simulating
  two concurrent sessions in one process) — each only ever touches its own
  directory; no cross-contamination.
- Bash background path (`runBashBackground`) covered by the same `cmd.Dir`
  assertion, not just the foreground path.

## Out of scope
- Sandboxing / restricting absolute paths to within the session's cwd — a
  separate, larger design decision (changes the trust model), not what was
  reported.
- Any change to `MemoWrite`/`MemoSearch`/`MemoDelete`/`ScheduleList` — already
  correctly cwd-aware.
