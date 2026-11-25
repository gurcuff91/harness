# Profiles move into the agent — design

Status: approved by the owner (2026-11-26). Target: v0.123.0 (breaking).

## Goal

A profile is a named bundle of directives and tools a session can be bound to.
Its registry and its application lived in `server`; the session's bound NAMES
and the fork's inheritance already lived in `agent`. Everything moves to
`agent/profiles` + `agent`, so an SDK user without a server gets profiles and
every frontend (server, TUI, ACP, tests) honors them the same way. The server
becomes a thin translator.

## Shape

- `agent/profiles`: `Profile{Directives []string, Tools func() []tools.Tool}` and a
  concurrency-safe `Registry` (`Register`, `Unregister`, `Names`, `Get`).
- `AgentOptions.Profiles` / `harness.AgentWithProfiles(map[string]profiles.Profile)`
  define them at construction; `Agent.RegisterProfile` / `UnregisterProfile` /
  `ProfileNames` change them live (active sessions bound to a changed profile are
  reconfigured at their next turn boundary).
- Session option `agent.WithSessionProfiles(names ...string)` for `NewSession`,
  `ResumeSession`, `ForkSession` (and import). It REPLACES, PATCH-style:

  | what the caller sends | the session's profiles |
  |---|---|
  | no option / no `profiles` field | unchanged (new session: none; fork: its parent's) |
  | one or more names | exactly those |
  | none (`WithSessionProfiles()` / `"profiles": []`) | none |

  A blank name is an error; nothing else is validated by the option itself.
- Creating a session with a name that is not registered FAILS and creates nothing.
  Opening one whose STORED profile is no longer registered skips it (the session
  stays usable), KEEPS the name in the meta (it applies again once registered) and
  reports it: `Session.MissingProfiles()`, `missing_profiles` in the session detail,
  a warning line in the TUI on resume.
- Fork inherits the parent's names (already true) and may replace them with the
  option; an import brings its names in the export and treats them like a resume.
- `server`: its own registry and `mergeProfiles` (additive resume) go away;
  `server.RegisterProfile` delegates to the agent; `profiles` in the create/resume
  body maps to `WithSessionProfiles` with the table above.

## Breaking

`server.Profile` → `profiles.Profile`; resuming over the API REPLACES the profile
list instead of adding to it. `Agent.SetSessionProfiles` stays (changes the stored
names without reopening).

## Tests

Registry (concurrency); create fails on an unknown name and leaves nothing; the
PATCH table on new/resume/fork/import; a missing stored profile is skipped, kept in
the meta and reported, and applies again after registering; directives and tools
reach only bound sessions; live register/unregister reconfigures active sessions;
API 400/200 and `missing_profiles`; TUI warning; mutants; full suite.
