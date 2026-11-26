---
title: Memory
section: Using Harness
order: 120
summary: Persistent project and global memory the agent writes and searches itself, stored in SQLite.
---

Memory lets the agent keep facts across sessions: decisions, conventions, gotchas, architecture notes. The agent writes and reads it through three tools; you read it with `harness memo`.

## How it works

When the agent has memory, its system prompt gets a short `## Memory` section telling it to search memory when it lacks context and to save durable insights (never transient task state). The tools are:

| Tool | Purpose |
| --- | --- |
| `MemoWrite` | Save a memory: `slug`, `content`, optional `global`. Reusing a slug overwrites that memory. |
| `MemoSearch` | Full-text search by `query` (ranked by relevance), or list all memories when `query` is omitted (most recently updated first). Supports `include_content` (default true), `skip` and `limit` (default 10). |
| `MemoDelete` | Delete a memory by `slug`; set `global` to delete a global one. |

Ask the agent to remember something and it will call `MemoWrite`:

```text
❯ remember that integration tests need DATABASE_URL set to the docker compose database
```

## Scopes

- **Project** (default): tied to the working directory of the session. Only sessions in that directory see it.
- **Global**: saved with `global: true`; surfaces in every project. Meant for personal conventions and preferences.

`MemoSearch` returns the current project's memories plus the global ones.

## Storage

Memories are stored in SQLite with full-text search at `~/.harness/agent/memory.db`. If the database cannot be opened, harness runs without memory instead of failing.

Sub-agents (see [Built-in tools](tools.md)) can search memory but cannot write or delete, so only the parent session decides what persists.

## Reading memories from the shell

`harness memo` is read-only:

```bash
$ harness memo                     # list this project's memories (plus global)
$ harness memo "database url"      # full-text search
$ harness memo --global            # only global memories
$ harness memo --all               # every project
$ harness memo --content --limit 5 # show a content preview, 5 per page
$ harness memo --skip 10           # next page
```

| Flag | Meaning |
| --- | --- |
| `--all` | Include memories from all projects. |
| `--global` | Only global memories. |
| `--content` | Show each memory's content preview. |
| `--limit N` | Results per page (default 10). |
| `--skip N` | Pagination offset (default 0). |

Writing and deleting are done by the agent, not from the CLI. The server also exposes memory CRUD under `/api/memories`; see the [HTTP API](../reference/http-api.md).

## SDK

An embedded agent has no memory unless you give it a store with `harness.AgentWithMemory(store)`; see [Options](../sdk/options.md).
