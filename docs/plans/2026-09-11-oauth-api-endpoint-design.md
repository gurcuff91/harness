# OAuth API endpoint — design

## Goal

Move the native OAuth PKCE login flows (`internal/oauthflow`) behind the
server's HTTP API, so both the CLI (`harness connect`) and the TUI stop
importing `oauthflow` directly and instead drive OAuth through
`client.Client`, exactly like every other client/server interaction in
harness. This is a pure refactor of *where the OAuth logic runs from*, not
a change to the OAuth protocol itself.

## Endpoint

```
POST /api/oauth/{provider}
```

One endpoint, two phases, both `POST`, differentiated by whether the body
carries `exchange_code`.

### Phase 1 — Start (no `exchange_code`)

```
POST /api/oauth/{provider}
Body: {} (or absent)

200 OK
{"auth_url": "https://...", "verifier_code": "<pkce verifier>"}
```

The server does NOT open a browser — that stays the client's
responsibility (CLI/TUI), same as who prints/copies the URL today.

### Phase 2 — Exchange (with `exchange_code`)

```
POST /api/oauth/{provider}
Body: {"exchange_code": "<code>", "verifier_code": "<from phase 1>"}

200 OK
{"access_token": "...", "refresh_token": "...", "expires_at": <unix_ms>, "account_id": "..."}
```

Exchange returns raw credentials only — it does NOT call `Connect()`. The
caller makes a separate call to `POST /api/providers/{name}/connect` to
persist them, exactly like `RunOAuth` + `ConnectProviderWithCreds` already
does today. Two endpoints, two responsibilities, unchanged.

## Statelessness between phases

The server holds NO session state between Start and Exchange. The PKCE
`verifier` generated in Start is returned to the caller and must be sent
back in Exchange's body. This is the mechanism that lets two independent
REST calls (potentially separated by however long the user takes to log
in and copy a code) complete a protocol that inherently needs the same
verifier used in both halves — without a `map[string]state` living in the
server's memory, without a session id, without a TTL cache to invalidate.

Rationale: PKCE requires the raw `code_verifier` (not just the `code`) at
token-exchange time, because the auth server recomputes SHA256(verifier)
and compares it against the `code_challenge` sent during Start — that's
the entire point of PKCE (RFC 7636), preventing a code interception attack
from succeeding without also knowing the verifier. The verifier can travel
however the implementation prefers; here it travels via the client.

## The `oauthflow.OauthFlow` interface — signature change

```go
type OauthFlow interface {
    // Start generates PKCE, returns the auth URL and the verifier the
    // caller MUST persist and pass back to Exchange. Does not open a
    // browser (moved to the client, see below) and does not save the
    // verifier internally (moved to the caller, for statelessness).
    Start() (authURL, verifierCode string, err error)

    // Exchange swaps the code for credentials using the SAME verifier
    // returned by this flow's Start call, passed in explicitly.
    Exchange(code, verifierCode string) (*types.Credentials, error)
}
```

Every flow (`claude.go`, `codex.go`) stops storing `f.verifier` as mutable
struct state that `Exchange` reads implicitly — it's now a plain parameter.
`openBrowser` is removed from the oauthflow package's public contract; the
one call site that used it (each flow's `Start`) is deleted. The function
itself moves to a new tiny package, `internal/browseropen`, used only by
CLI and TUI — never by the server.

## Codex's local listener (`localhost:1455`)

Only `codex-oauth` binds a local HTTP listener; Claude's OAuth callback is
hosted by Anthropic (`https://platform.claude.com/oauth/code/callback`)
and stays that way — **confirmed live** that Anthropic's OAuth authorize
endpoint rejects `localhost:1455` as a redirect_uri for the public
Claude Code client id with `"Redirect URI ... is not supported by
client."` (tested against the real endpoint with a valid PKCE challenge).
Unifying both providers under one local-listener mechanism is not
possible; Codex keeps its listener, Claude keeps its hosted callback page.

The listener is entirely internal to the server's Start handler for
`codex-oauth`:

- Synchronous bind of `localhost:1455` BEFORE responding to the caller
  (same anti-race-condition ordering already validated: a fast ChatGPT
  redirect can land before an async bind would win the race).
- Bind failure → `409 Conflict` ("port 1455 already in use — a login is
  already in progress"), nothing else happens, no second listener.
- Bind success → a goroutine serves the one-shot callback page (renders
  the code, offers a Copy button), then self-shuts-down on the FIRST
  request served.
- **New**: a 5-minute timeout (`time.AfterFunc`) also shuts the listener
  down if no callback ever arrives (user abandons the login, browser
  never redirects, etc.) — prevents the port staying bound indefinitely.
  Cancelled if the real callback arrives first.
- Dies for free with the parent server process (it's a goroutine; Go
  doesn't need explicit cleanup for process exit).
- The client never knows this listener exists — it only ever sees
  `auth_url` + `verifier_code` from Start's response.
- Exchange does NOT depend on the listener being alive: it talks directly
  to `auth.openai.com`'s token endpoint. The listener's only job is
  displaying the code to the user; it is not part of the Exchange path.

## Client SDK additions (`client/client.go`)

```go
// StartOAuth begins a provider's native OAuth flow: returns the URL to open
// in a browser and the PKCE verifier the caller must hold and pass back to
// ExchangeOAuth. The server is stateless between the two calls.
func (c *Client) StartOAuth(provider string) (authURL, verifierCode string, err error)

// ExchangeOAuth swaps the user-pasted code for credentials, using the
// verifier StartOAuth returned. Returns raw credentials — the caller is
// still responsible for calling ConnectProviderWithCreds to persist them.
func (c *Client) ExchangeOAuth(provider, exchangeCode, verifierCode string) (*types.Credentials, error)
```

## Consumer migration

- `internal/cli/oauth.go` (`RunOAuth`): stops calling `oauthflow.For`
  directly. Calls `client.StartOAuth`, opens the browser itself via
  `internal/browseropen`, prompts for the pasted code, calls
  `client.ExchangeOAuth`. Still returns `*types.Credentials` to its
  caller (`RunConnect`), which still separately calls
  `ConnectProviderWithCreds` — unchanged from today's shape.
- `internal/tui/commands.go` (`cmdConnect`, `completeOAuthConnect`):
  same substitution. `pendingValue.oauthFlow oauthflow.OauthFlow` becomes
  `pendingValue.verifierCode string` — the TUI no longer imports
  `oauthflow` at all.
- `internal/oauthflow` becomes purely a server-side implementation detail,
  imported only by `server/oauth.go`.

## Error handling

| Case | Status | Body |
| --- | --- | --- |
| Unknown provider / no OAuth flow | 404 | `"no OAuth flow for provider: X"` |
| Codex port 1455 busy (Start only) | 409 | `"port 1455 already in use — a login is already in progress"` |
| Exchange missing `verifier_code` | 400 | `"verifier_code is required for exchange"` |
| Exchange fails upstream (bad/expired code) | 502 | the provider's own error message, wrapped |

## What does NOT change

- `POST /api/providers/{name}/connect` — untouched; still receives fully
  resolved credentials and persists them via `Provider.Connect`.
- The PKCE/token-exchange logic itself (`generatePKCE`, `postToken`,
  Codex's callback page HTML) — only the plumbing around it moves.
- No new dependency, no persisted OAuth state on disk, no session id, no
  TTL cache.

## Tests

- `internal/oauthflow/*_test.go`: adjust for the new `Start()`/`Exchange`
  signatures; verifier passed explicitly instead of read from struct
  state; confirm two flow instances with different verifiers never
  cross-contaminate.
- `server/oauth_test.go` (new): Start for codex-oauth binds 1455 and
  returns both fields; a second concurrent Start returns 409; Start for
  claude-oauth returns fields WITHOUT binding any port; Exchange missing
  `verifier_code` returns 400; unknown provider returns 404; Exchange
  failure surfaces the upstream error as 502; the 5-minute listener
  timeout is exercised via an injectable/overridable duration (same
  pattern as today's `openBrowser` test stub) instead of a real 5-minute
  wait in CI.
- `client/oauth_test.go` (new): `StartOAuth`/`ExchangeOAuth` against an
  `httptest` server, verifying request/response shapes.

All existing tests continue to pass (`go test ./...`), `go vet ./...`
clean.
