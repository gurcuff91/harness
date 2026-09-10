# `codex-oauth` provider — ChatGPT subscription access via Codex CLI emulation — Design

**Date:** 2026-09-06
**Status:** Approved (brainstorm Q&A complete), ready for implementation
**Area:** `internal/providers/codex_oauth.go` (new), `internal/oauthflow/codex.go` (new),
`internal/providers/{registry,status}.go` (registration), `internal/oauthflow/oauthflow.go`
(`For()` case), `internal/cli` (connect handler), `types/credentials.go` (+`AccountID`),
`internal/config/credentials.go` (+`AccountID`)

## Problem

Requested by Gus: a provider equivalent to `claude-oauth`, but for OpenAI —
using the ChatGPT subscription's included Codex usage instead of pay-per-token
API credits. OpenAI's public API (`api.openai.com`, already supported by the
`openai` provider) is usage-based only; subscription access exists exclusively
through the Codex backend (`chatgpt.com/backend-api/codex/responses`), the
same endpoint the Codex CLI itself uses after "Sign in with ChatGPT".

Research (web) established the full landscape before designing:

- Zed ships the same feature (`openai_subscribed.rs`, merged PR #53166) and is
  **sanctioned by OpenAI** ("work with OpenAI... continues to support
  subscription-based access for third-party tools" — their blog), using
  OAuth PKCE with the Codex CLI's **public client_id**
  (`app_EMoamEEZ73f0CkXaXp7hrann`) and its own registered `originator: zed`.
- The backend enforces a **server-side originator allowlist**: first-party
  values (`codex_cli_rs`, `codex_vscode`, `codex_sdk_ts`, `Codex ` prefix)
  pass; arbitrary third-party values (`originator: pi` — pi issue #1828)
  receive 403 on every request; even the authorize page refuses to render
  for unregistered originators (lingtai commit evidence).
- There is no documented third-party auth contract (openai/codex issue
  #36886 asking for exactly this remains open, unanswered).
- brokk-anvil-client and the proxy ecosystem talk to the same endpoint by
  **emulating `codex_cli_rs`** — the same posture harness already takes with
  `claude-oauth` (emulating Claude Code).

**Decision (Gus)**: Option A — full Codex CLI emulation
(`originator: codex_cli_rs`, `user-agent: codex_cli_rs/<version>`), symmetric
with `claude-oauth`'s "fingimos ser Claude Code". Accepted risk profile is
identical to the existing provider: private endpoint, no documented contract,
enforcement can tighten at any time (exactly like the `claude_code_version_too_old`
incident harness already lived through and fixed via `CLAUDE_CLI_VERSION`).

## Architecture

Mirrors `claude_oauth.go` (787 lines) shape-for-shape — one new provider
file, one new oauth flow file, one `For()` case, one registry/status entry.
No new dependencies (stdlib only), no interface changes to `Provider`.

### Identity constants

```go
const (
    codexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
    codexModelsURL    = "https://chatgpt.com/backend-api/codex/models"
    codexTokenURL     = "https://auth.openai.com/oauth/token"
    codexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann" // Codex CLI's public PKCE client
)
var codexVersion = envOrDefault("CODEX_CLI_VERSION", "0.144.0")
```

- `codexVersion` feeds BOTH the `user-agent` (`codex_cli_rs/<v> (external, cli)`)
  and the `client_version` query param on `GET /models` — the same
  server-side version gating per model (`minimal_client_version`) that
  bit harness on the Claude side. Hardcode + env override, identical pattern
  to `CLAUDE_CLI_VERSION` (v0.76.63/64).
- Headers per request: `Authorization: Bearer <access_token>`,
  `originator: codex_cli_rs`, `ChatGPT-Account-ID: <account_id>`,
  `session_id: <uuid-per-session>`. NOT `api.openai.com` — the OAuth token
  only works against the chatgpt.com Codex backend.

### `account_id` — new `AccountID` field (Gus decision)

The backend requires `ChatGPT-Account-ID` on every call. Source: the
`id_token` JWT returned by the token exchange — its payload carries
`chatgpt_account_id` (verified live against Gus's own `~/.codex/auth.json`).
Extraction with Zed's three-level fallback, verbatim order:
1. `chatgpt_account_id` (top-level claim)
2. `["https://api.openai.com/auth"].chatgpt_account_id`
3. `organizations[0].id`

JWT payload is base64url-decoded without signature verification — legitimate
here because the token arrives directly from `auth.openai.com` over TLS
(same trust model as every OAuth client that doesn't re-verify id_token
signatures locally).

Plumbing changes:
- `types.Credentials` + `AccountID string \`json:"account_id,omitempty"\``
  (OAuth-only field, alongside SubscriptionType).
- `types.OAuthCredentials(...)` constructor gains an `accountID` param —
  updated at all call sites.
- `internal/config/credentials.go`'s `ProviderCredential` + `AccountID`
  likewise (persisted to `credentials.json` under the `codex-oauth` key,
  same file, same filelock hardening — no new storage).
- `claude-oauth` passes `""` for accountID (its flow has no account id).

### OAuth flow (`internal/oauthflow/codex.go`)

Same single-use PKCE flow shape as `claude.go`:

- **Start**: authorize URL `https://auth.openai.com/oauth/authorize?`
  with `client_id`, `response_type=code`, `code_challenge` (S256),
  `scope=openid profile email offline_access api.connectors.read api.connectors.invoke`,
  `originator=codex_cli_rs`, `codex_cli_simplified_flow=true`,
  `state` (CSRF). Opens browser; caller prints URL for manual open.
- **Redirect URI**: the Codex CLI's registered localhost callback
  (`http://localhost:1455/auth/callback`). User copies the code shown in the
  browser's callback page and pastes it back — identical UX to
  `claude-oauth`'s connect flow (value-capture mode in the TUI). No local
  HTTP listener (deliberately out of scope: YAGNI, and the copy/paste flow
  already exists end-to-end).
- **Exchange**: POST form-urlencoded to `codexTokenURL`
  (`grant_type=authorization_code`, `client_id`, `code`, `code_verifier`,
  `redirect_uri`). Response: `access_token`, `refresh_token`, `id_token`,
  `expires_in` (~hours). The shared `postToken` helper currently posts JSON;
  `auth.openai.com` requires form-urlencoded, so codex.go gets its own small
  token POST (or `postToken` gains a content-type param — implementation
  choice, not a design change). `id_token` is decoded and `AccountID`
  extracted (above) before constructing `types.Credentials`.

### Provider (`internal/providers/codex_oauth.go`)

`CodexOAuth` struct mirrors `ClaudeOAuth`: `client`, `tokens *tokenManager`,
`session string` (uuid — sent as `session_id` header, stable per harness
instance for prompt-cache steering, per brokk's measured evidence),
`cache map[string]types.ModelMeta` + `mu`. Name `"codex-oauth"`,
DisplayName `"Codex OAuth"`, `CredTypeOAuth`.

- **Credentials**: same chain as claude-oauth — cache →
  `credentials.json` (`codex-oauth` key) → refresh via
  `CredentialsManager.UpdateCredential` RMW+filelock (the v0.76.39 hardened
  path) against `auth.openai.com/oauth/token` with
  `grant_type=refresh_token`. Fatal 400/401/403 → clear creds, re-login
  required (Zed's classification, and OpenAI rotates refresh tokens — a
  consumed-token double-redeem is permanent, the same corruption vector
  claude-oauth's single-endpoint rule exists to prevent).
- **Models**: `GET codexModelsURL?client_version={codexVersion}` → keep
  `visibility == "list"`, sort by `priority` desc. Map
  `default_reasoning_level`/`supported_reasoning_levels` to thinking levels.
  No context window/pricing in the response — falls through the existing
  capability cascade (registry tiers 3-4). Cloudflare can 403 or even
  200-with-HTML on `/models`; surface a loud parse error (don't cache garbage).
- **Request translation** (`CompleteStream`): internal format → Codex
  Responses dialect:
  - `store: false` (mandatory — backend 400s otherwise), `stream: true`,
    `include: ["reasoning.encrypted_content"]`.
  - System messages hoisted to top-level `instructions` (joined `"\n\n"`),
    NOT as input items (backend requirement, Zed-verified).
  - Tools: flat schema (`name`/`description`/`parameters` top-level, no
    `function` nesting), `tool_choice: "auto"`, `parallel_tool_calls: true`.
  - `max_output_tokens` omitted entirely (backend rejects it — Zed's comment
    verbatim). `maxTokens` from AgentOptions is silently dropped for this
    provider, documented as such.
  - Assistant reasoning replay: `reasoning` input items with
    `encrypted_content` + `summary: []` (empty summary MUST be sent —
    400 otherwise), same DeepSeek-style replay constraint pattern.
  - Thinking: `reasoning: {effort}` mapped off|low|medium|high|xhigh →
    off→omit, xhigh→high (no OpenAI xhigh).
- **SSE parsing**: NEW `ParseCodexStream` (distinct event dialect — do NOT
  reuse `parseOpenAIStream`): `response.output_text.delta` → text,
  `response.reasoning_text.delta` / `response.reasoning_summary_text.delta`
  → thinking, `response.output_item.done` → completed tool calls,
  `response.completed`/`response.failed` → final usage
  (`input_tokens`/`output_tokens` — NOT prompt_tokens/completion_tokens)
  + natural end marker, `response.in_progress` etc. ignored. Same dropped-
  connection hardening contract as v0.76.53: a channel close without
  `response.completed` is an error, not success.
- **401 retry**: refresh-once with the same stable `session_id` (a refresh
  must not cost the prompt cache), mirroring claude-oauth's retry shape.

### Registration

- `registry.go`: `NewCodexOAuth()` right after `NewClaudeOAuth()` (both are
  credentials-first providers).
- `status.go`: `order = ["claude-oauth", "codex-oauth", "anthropic", "openai", ...]`.
- `oauthflow.For()`: `case "codex-oauth": return NewCodexOauthFlow(), nil`.
- `internal/cli` connect handler: the existing `/connect <provider>` OAuth
  path dispatches via `oauthflow.For()` — the provider already needs a
  `CredentialType() == CredTypeOAuth` connect handler entry; add codex-oauth
  to that switch (per "Adding a New Provider" in AGENTS.md).

## Testing

- Unit (mocked HTTP, following claude_oauth's test shape): token exchange
  (incl. `AccountID` JWT extraction with all 3 fallbacks), refresh RMW+filelock,
  request translation (instructions hoisting, flat tools, no max_output_tokens,
  store:false, reasoning replay with summary:[]), SSE parser against fixture
  events (text/thinking/tool/completed/failed/no-marker→error), models fetch
  (visibility filter, priority sort, client_version param, Cloudflare HTML
  → loud error).
- Live validation by Gus with his ChatGPT account after implementation
  (his decision): run the real `/connect codex-oauth` flow, verify models
  appear and a real turn streams — the same bar `native-oauth-flow-validated`
  set for claude-oauth. Note: his current `~/.codex/auth.json` login is
  plan-free and its refresh token is dead — the harness-native OAuth flow
  makes that irrelevant (fresh login through OUR client_id usage), but his
  account must have a paid ChatGPT plan for models to be usable.

## Out of scope (deliberately)

- No local HTTP callback listener (copy/paste code flow only).
- No `/v1/chat/completions` translation — Codex Responses dialect only.
- No WebSocket transport (Codex CLI's `responses_websockets` beta) — SSE first.
- No `service_tier`/priority fast-mode exposure — default tier only.