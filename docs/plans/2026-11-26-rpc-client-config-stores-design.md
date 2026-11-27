# RPC: settings and credentials stores served by the client (`kind: "client"`)

Date: 2026-11-26 · Target: v0.133.0 · Owner-approved. First of the pieces the client may implement (then memory, resources, store).

## 1. Kinds and naming
Omitting a field = the default of `agent.New`; a `kind` is only sent to choose something else.
| Field | Omitted | Kinds |
|---|---|---|
| `settings` | `file` (~/.harness/settings.json) | `file`, `ephemeral`, `client` |
| `credentials` | `file` (~/.harness/credentials.json, 0600) | `file`, `ephemeral`, `client` |
`client` = the data lives in the client; the binary makes requests to it. `ephemeral` = in memory, never touches disk. Each field chooses separately (secrets in a secret manager, settings in files). Same release, same fields: `store: {kind:"memory"}` is renamed `ephemeral` (it clashed with the `memory` field) and `memory: {kind:"none"}` is removed (omit the field).

## 2. Protocol (binary → client), two domains with the same five methods
```
settings/get     {namespace, key}                              → {found, value, version}
settings/set     {namespace, key, value}                       → {}
settings/delete  {namespace, key}                              → {}
settings/list    {namespace}                                   → {entries: {<key>: {value, version}}}
settings/swap    {namespace, key, expected_version, value}     → {swapped}
credentials/get | set | delete | list | swap                   (identical)
```
`value` is the JSON value verbatim (the store never interprets it). `version` is an opaque string the client chooses (a counter, an etag, a hash, a row version); the binary only hands it back in `swap`. `swap`: `expected_version: null` = must not exist, `value: null` = delete; a version mismatch answers `swapped:false` and changes nothing. No `Close` method: the binary closes the connection and the client releases its own resources.
The Go adapter implements `configstore.SettingsStore`/`CredentialsStore` over these. Its `SwapValue(ns, key, fn)` is a compare-and-swap loop (at most 10 attempts): get (value, version) → run `fn` IN THE BINARY → `swap(version, next)` → retry on conflict; exhausted = a `conflict` error. The callback never crosses the pipe and the client implements one simple atomic operation (`UPDATE … WHERE version = ?`, conditional write). The OAuth refresh keeps its guarantee: of two concurrent redeemers of a single-use refresh token only one wins the swap, the other re-reads the renewed token.

## 3. Start order
`initialize`: check `protocol_version` → validate the config (the error names the field) → if settings/credentials are `client`, register the adapters (`SetSettingsStore`/`SetCredentialsStore`) → build the agent (it READS settings) → answer. The client must therefore answer `settings/get` WHILE `initialize` is still pending (each request runs in its own goroutine, so the binary can call the client before it has replied). A failure after the stores were registered ends the process (they are process-global and cannot be swapped): it is not retryable.

## 4. Errors and timeouts
- The adapter logs every error on stderr; none is silent.
- **A transport failure is not "not found".** `internal/config` readers used to turn ANY store error into "absent" (`getJSON`). `CredentialsManager.Credential` (and `APIKey`, `OAuth`) now report the error (`Credential(provider) (cred, found bool, err error)`), and the provider code that resolves credentials treats an error as "could not read" instead of "not connected". Local file stores never fail this way, so their behavior is unchanged.
- Each request to the client has a deadline (30 s). On expiry: `the client did not answer settings/get within 30s: a "client" store must be served from the first message`; during `initialize` that fails it.

## 5. Not in this change
No cache in the adapter (a second state that can drift); no cross-process protection (a client store is atomic only as far as the client makes `swap` atomic: documented); no `Close`; `configstore`'s interfaces do not change (the adapter is a third implementation next to FileStore and InMemoryStore).

## 6. Tests
The adapter passes `configstore`'s contract (exported as a reusable function: the same bar as FileStore and InMemoryStore). Protocol: `initialize` with a `client` store served during start-up, timeout, client error, swap conflict and retry, two concurrent OAuth refresh swaps (one wins). Mutants on the retry loop, the registration order and the error-vs-not-found distinction. The new methods enter the schema and the generated reference.
