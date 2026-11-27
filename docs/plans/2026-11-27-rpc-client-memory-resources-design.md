# RPC: memory and resources served by the client (`kind: "client"`)

Date: 2026-11-27 · Target: v0.134.0 · Owner-approved. Follows 2026-11-26-rpc-client-config-stores-design.md; the store of sessions is the last piece and is NOT in this release.

## 1. Kinds
| Field | Omitted | Kinds |
|---|---|---|
| `memory` | no memory | `sqlite`, `client` |
| `resources` | discovers nothing | `file`, `client` |
`client` registers an adapter in the binary that calls the client: for `memory` a `memory.Store` over `memory/*`, for `resources` a `ResourceLoader` over `resources/load`. Unlike the config stores these are PER AGENT (not process-global): there is no registration order to respect and no readiness probe is needed before the agent exists. Sub-agents share the parent's memory (read-only) and its resource loader, so with `client` they also call the client, possibly in parallel: the client must serve these requests concurrently (as it already must for `tool/call`).

## 2. memory (binary → client)
```
memory/write   {cwd, slug, content, global}                          → {created}
memory/get     {cwd, slug, global}                                   → {found, memory}
memory/search  {cwd, query, include_content, skip, limit}            → memory.SearchResult
memory/delete  {cwd, slug, global}                                   → {deleted}
```
No `Close` method: the binary closes the connection. `memory.Memory` and `memory.SearchResult` already carry snake_case tags and travel unchanged. Scoping contract the client must honor (it is the Store's): `global: true` stores under the `<global>` cwd; `search` with cwd `""` = all projects, `<global>` = only global ones, any other = that project's PLUS the global ones; empty `query` = list mode, most recently updated first (no `score`); text = relevance search (`score` set, higher = more relevant); `limit <= 0` = 10, `skip < 0` = 0; `include_content: false` omits `content`; `total` counts all matches across pages.
The agent searches `("", 31)` of the session's cwd every time it opens or reconfigures a session (the slug list of the prompt): one round trip per open. If that read fails the agent leaves the list out (memory is a convenience); the `MemoSearch`/`MemoWrite` tools return the error to the model.

## 3. resources (binary → client)
```
resources/load   {cwd}   → {system_md, agents_md, skills: [{name, description, location?, content}]}
```
`resources.Resources` and `resources.Skill` get snake_case JSON tags (`system_md`, `agents_md`); `location` is optional (a client that keeps skills in a database has no path; the Skill tool then reports no directory). Called once per session open/resume/fork/reconfigure. A failing `Load` leaves a resumed/forked session without context but makes `NewSession` fail (a new session's user expects to see the failure) — unchanged from today. The reply carries every skill's content (about 264 KB on the owner's machine): reasonable for one JSON-RPC message.

## 4. The memory contract (portable only)
`memory/storetest.RunContract(t, newStore)` (public, like `configstore/storetest`) runs every case on a fresh store; SQLiteStore, an in-memory reference fake and the RPC adapter must pass it. It requires only what is portable: upsert + `created` (true the first time, false on a rewrite); global vs project scoping (Write/Get/Delete); `Get` of a missing entry = not found without error; delete reports whether one existed; search scoping (all / `<global>` / project + global); list mode ordered by `updated_at` descending; a text search by a WHOLE word finds exactly the memories that contain it (case-insensitively) and none of the others; **relevance is checked by ORDER, never by the value of `score`**: a memory that repeats the word ranks before one that mentions it once (measured on SQLite: with a handful of short memories BM25 scores round to 0.00, so the numbers cannot be required to discriminate); pagination (`skip`, `limit`, the default of 10, `total` counting all matches across pages); `include_content:false` leaves `content` empty; every result carries its `cwd`; concurrent writes lose nothing. It does NOT require SQLite's prefix matching (`dep` finding `deploy`), an exact ranking among close matches, or any score value.

## 5. Errors and timeouts
Same as the config stores: every error from the client is logged on stderr and returned; 30 s deadline per request: `the client did not answer memory/search within 30s`. A closed connection fails the call with `the client disconnected`.

## 6. Tests
Contract against SQLite, an in-memory reference fake and the adapter (backed by a correct client). Protocol: `initialize` with both `client`, a full turn where the model writes and searches memory THROUGH the client (a fake provider issuing MemoWrite/MemoSearch), the skills of a session served by `resources/load` and the Skill tool reading one, timeout, client error, a failing `resources/load` on new vs resume, concurrent sub-agent calls (`-race`). Mutants on the scoping, the null/absent handling and the error paths. New methods in the schema and the generated reference. Live check with a real client against the installed binary.
