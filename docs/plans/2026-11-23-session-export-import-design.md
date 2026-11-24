# Session export / import — design

Status: approved by the owner (2026-11-23).

## Goal

Move a session's whole conversation into a file and back: export writes a
self-describing JSONL; import creates a NEW session (never destructive, like a
fork) identical to the exported one.

## 1. Format and storage

File `<sanitized-name>-<id8>.export.jsonl` (lower case, every run of non
letters/digits → `-`, ≤ 50 chars, `session` when empty; exporting the same
session again overwrites its own file):

```
{"format":"harness-session-export","version":1,"exported_at":"…Z","harness":"v…","meta":{…SessionMeta…}}
{"role":"user",…}            ← every message of the log, as stored (pre-compaction too)
```

Sessions can be huge (640 MB / 136k messages measured), so both directions
stream. Two OPTIONAL store capabilities (like `WindowReader`), with a generic
fallback so any `SessionStore` works:

- `MessageStreamer.StreamMessages(id, fn(raw []byte) error)` — FileStore hands
  the raw lines (a faithful copy, no re-encode). Fallback: `LoadMessages` + encode.
- `MessageImporter.ImportMessages(id, next)` — FileStore writes the whole log in
  one pass (buffered, fsync, rename). Fallback: one `AppendMessage` per message.

Import validation: header first with the right `format`/`version`, `meta`
present, every following line a `types.Message`, `meta.CompactOffset ≤ count`.
Any failure names the line and creates NOTHING (the partial session is deleted).

## 2. Agent and API

- `Agent.ExportSession(id, w) (n, err)`: header + streamed messages; works on an
  open session (live meta values, never blocks a turn) or a closed one.
- `Agent.ImportSession(r, cwd, opts...) (*Session, error)`: validates, creates a
  NEW session (new id), stores the messages, opens it. Kept from the meta: name,
  model, thinking, max iterations, profiles, compaction (offset/count/reset
  count), stats. New: id, created/last-active (now), cwd (given, else the
  meta's). An unavailable model falls back as in ResumeSession. Accepts
  `WithSessionChannels`.
- API, CONTENT only (one universal way, no server paths):
  - `GET /api/sessions/{id}/export` — streams the JSONL (`application/x-ndjson`,
    `Content-Disposition` with the proposed file name).
  - `POST /api/sessions/import[?cwd=]` — the JSONL as the body (≤ 1 GB → 413,
    `ServerOptions.ImportMaxBodyBytes`); 201 with the new session.
  - 404 no session, 400 invalid file (line number), 413 too large.
- `client`: `ExportSession(id, w) (filename, n, err)`, `ImportSession(r, opts...)`.
- Session COMMANDS (what the TUI uses, via `POST /api/sessions/{id}/commands`):
  - `export` (no params): the server writes `~/.harness/exports/<file>` (dir 0700,
    file 0600, temp + rename) and answers the path and count in `message`. No
    path parameter: nobody can make the server write elsewhere.
  - `import` `{path}`: the server reads that file (format-validated before
    anything else) and answers the NEW session's id in `message` (option a).

## 3. TUI, errors, tests

- `/export` → shows the path and count. `/import <path>` (`~` and paths relative
  to the session's cwd are expanded by the TUI) → switches to the new session
  like `/fork` (history rendered); the old one is untouched. Both in the palette.
  An imported session listens to no channel.
- Errors as above; export during a turn reads what is written; nothing partial
  is ever left (temp file, deleted partial session).
- Tests: round trip byte-identical (images, thinking, tools, created_at), meta
  kept/regenerated, compacted window preserved; every validation error leaves
  nothing; multi-MB line; streaming (no full load); fallback paths on
  InMemoryStore; API (GET/POST, 201/400/413); commands; TUI switch; mutants;
  live with a real session.
