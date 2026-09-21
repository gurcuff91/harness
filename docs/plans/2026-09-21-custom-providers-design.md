# Custom OpenAI-compatible providers

## Context

Today `internal/providers.All` is a FIXED list of built-in providers
(`anthropic`, `claude-oauth`, `codex-oauth`, `minimax`, `ollama-cloud`,
`ollama`, `openai`, `opencode-go`), constructed once by `initRegistry()`
(guarded by `sync.Once`) at process start. There is no way to point harness
at a proxy or self-hosted endpoint that speaks the OpenAI Chat Completions
dialect (LiteLLM, vLLM, a corporate gateway, a local llama.cpp server, …)
without hardcoding a new `internal/providers/<name>.go` file per deployment.

Every piece of infrastructure needed to support this already exists and
already works genuinely generically over `providers.All`:
`GET /api/providers` (server.go's `handleProviders`), `harness connect
<name> <key>` (`RunConnect`, already provider-name-driven via the API, not a
hardcoded switch), `provider/model` resolution (`providers.Resolve`), and
the credential storage cascade (`resolveAPIKey`/`storeAPIKey`/
`deleteCredential`, already keyed by provider *name* string, not a fixed
enum). None of that needs to change — the only gap is *constructing* a
`Provider` instance from user configuration instead of a Go source file.

MCP servers (`types.MCPServer`, `SettingsManager.MCPServers()`,
`mcp.Manager.Start()`) are the exact structural precedent: a keyed
collection in `settings.json`, read once at `Agent.New()` time (no hot
reload — adding/editing a server requires a process restart, which is an
accepted, already-shipped trade-off for MCP and will be for custom
providers too).

## Scope

**In scope:** `type: "openai"` custom providers only — an OpenAI Chat
Completions-compatible HTTP endpoint, activated by API key (never OAuth).
`type: "anthropic"` is explicitly deferred — `Anthropic` (unlike `OpenAI`)
has no configurable `baseURL` field today and would need that refactor
first; not worth doing until a real "anthropic-dialect proxy" need shows
up.

**Out of scope:** hot-reloading providers without a restart (matches MCP's
existing behavior); any model-capability filtering beyond what
`llm.EnrichMeta`'s existing OpenRouter-catalog/hardcoded-registry/
name-inference cascade already provides.

## Data shape

New `types.CustomProvider` (same file/pattern as `types.MCPServer`):

```go
type CustomProvider struct {
	Type      string            `json:"type"`                 // "openai" — only supported value for now
	URL       string            `json:"url"`                  // base URL for chat completions
	ModelsURL string            `json:"models_url,omitempty"` // optional; falls back to <url>/models
	Headers   map[string]string `json:"headers,omitempty"`    // optional extra HTTP headers, sent on every request
	Display   string            `json:"display,omitempty"`    // optional human-friendly name; falls back to the map key
	Disabled  bool              `json:"disabled,omitempty"`   // enabled by default; set true to skip
}
```

`settings.json` gains a new top-level keyed collection, sibling to `"mcp"` —
named `"provider"` (singular), matching `"mcp"`'s own singular naming style
exactly:

```json
{
  "provider": {
    "my-proxy": {
      "type": "openai",
      "url": "https://my-proxy.internal/v1",
      "headers": { "X-Org-Id": "acme" },
      "display": "Acme Internal Proxy"
    }
  }
}
```

The map key (`"my-proxy"` above) IS the provider name used everywhere else
(`harness connect my-proxy <key>`, `my-proxy/<model>` in `provider/model`
strings, credential storage key). No separate `name` field inside the
struct — same as MCP servers.

## Persistence — `internal/config/settings.go`

Mirrors the MCP section exactly, including the singular JSON key style
(`"mcp"`, not `"mcp_servers"` — so `"provider"`, not `"custom_providers"`):

- `settingsData` gains `Provider map[string]types.CustomProvider
  \`json:"provider,omitempty"\``.
- `SettingsManager.CustomProviders() map[string]CustomProvider` — defensive
  copy, same as `MCPServers()`.
- `SettingsManager.CustomProvider(name string) (CustomProvider, bool)`.
- `SettingsManager.SetCustomProvider(name string, cfg CustomProvider) error`
  — validates via `validateCustomProvider`, then the same
  lock-reload-mutate-save sequence as `SetMCPServer` (`AcquireFileLock` →
  `m.load()` → mutate → `m.save()`).
- `SettingsManager.DeleteCustomProvider(name string) error` — same pattern
  as `DeleteMCPServer`.
- New sentinel `ErrInvalidCustomProvider = errors.New("invalid custom provider")`,
  detectable via `errors.Is` for a 422 mapping (mirrors `ErrInvalidMCPServer`).

`validateCustomProvider(name string, cfg CustomProvider) error` enforces:
1. `cfg.Type` must be exactly `"openai"` (case-sensitive) — any other value
   (including empty) is rejected with a message naming `"openai"` as the
   only supported type for now.
2. `cfg.URL` must be non-empty.
3. `name` must not collide with a RESERVED built-in provider name:
   `anthropic`, `claude-oauth`, `codex-oauth`, `minimax`, `ollama-cloud`,
   `ollama`, `openai`, `opencode-go`. A small `reservedProviderNames` set
   literal in `settings.go` (or `internal/providers`, re-exported — TBD at
   implementation time, whichever avoids an import cycle more cleanly)
   enforces this centrally so it can never drift from the real registry.

## Provider implementation — `internal/providers/custom_openai.go`

New type `CustomOpenAI`, structurally `OpenAI`'s twin but parametrized
instead of hardcoded:

```go
type CustomOpenAI struct {
	name        string
	displayName string
	baseURL     string
	modelsURL   string            // "" means derive <baseURL>/models at fetch time
	headers     map[string]string // sent on EVERY request (chat + models listing)
	apiKey      string
	client      *http.Client
	cache       map[string]types.ModelMeta
	mu          sync.RWMutex
}

func NewCustomOpenAI(name string, cfg types.CustomProvider) *CustomOpenAI
```

- `Name()` → the configured map key. `DisplayName()` → `cfg.Display` if
  non-empty, else the same name (mirrors `ModelsURL`'s own fallback
  pattern — one fallback idiom, applied twice).
- `CredentialType()` → always `types.CredTypeAPIKey`. `ResolveCredentials`/
  `Connect`/`Disconnect` reuse the SAME shared helpers every built-in
  api-key provider already uses (`resolveAPIKey`/`storeAPIKey`/
  `deleteCredential`), keyed by the custom provider's own `name` — NO
  environment-variable fallback (`resolveAPIKey` takes an env var name;
  custom providers pass `""` or a dedicated no-env variant, TBD at
  implementation — there's no sane env var name to invent per arbitrary
  custom provider, so this is credentials-file-only, exactly like the CLI
  `harness connect` flow already assumes for api-key providers without a
  well-known env var).
- `CompleteStream` → `llm.DoOpenAIStream(ctx, c.client, c.baseURL+"/chat/completions", c.apiKey, &llm.OpenAIRequest{Request: req}, c.headers, cb)` — the ONLY
  difference from `OpenAI.CompleteStream` is passing `c.headers` as
  `extraHeaders` instead of `nil`. `DoOpenAIStream` already accepts and
  merges `extraHeaders` — zero changes needed to the shared streaming
  dialect code.
- `FetchModels` → **brand-new function** (`fetchCustomOpenAIModels`), NOT a
  call to `fetchOpenAIModels` with a flag. Requests `modelsURL` (or
  `baseURL+"/models"` if unset), sends `headers` on that request too,
  parses the same `{"data": [{"id": ...}]}` shape, and — critically —
  accepts EVERY returned ID with **no `isOpenAIChatModel` prefix filtering
  at all**. Each accepted ID still goes through `llm.EnrichMeta` for
  capability/pricing enrichment (OpenRouter catalog → hardcoded registry →
  name inference → generic defaults), same as every other provider.
  Keeping this as an independent function (not a parametrized version of
  `fetchOpenAIModels`) makes it structurally impossible for a future edit
  to `fetchOpenAIModels` to accidentally reintroduce OpenAI-specific
  filtering into the custom-provider path — confirmed necessary live: a
  custom provider fronting, say, a MiniMax-model proxy would return 0
  models if `isOpenAIChatModel`'s `gpt-`/`o1`/`o3`/`o4`/`chatgpt-` prefix
  filter were ever applied to it.

## Registry wiring — `internal/providers/registry.go`

`initRegistry()` gains one more step after the fixed built-in list:

```go
func initRegistry() {
	All = []Provider{}
	// ... existing OAuth + built-in construction, unchanged ...

	for name, cfg := range config.GetSettingsManager().CustomProviders() {
		if cfg.Disabled {
			continue
		}
		if reservedProviderNames[name] {
			continue // defense in depth; SetCustomProvider already rejects this at write time
		}
		All = append(All, NewCustomOpenAI(name, cfg))
	}
}
```

A `Disabled: true` custom provider is skipped entirely — never constructed,
never appears in `providers.All`, `GET /api/providers`, or is resolvable
via `provider/model` — identical behavior to a disabled MCP server. Same
"no hot reload" rule as the rest of `initRegistry()`: enabling/disabling/
editing a custom provider takes effect on the next process start, not the
current one.

## HTTP API — `server/server.go`

Exact structural mirror of the MCP server endpoints, including the
singular path segment (`/api/settings/mcp/{name}` → `/api/settings/provider/{name}`,
not `/custom-providers/{name}`):

- `GET /api/settings/provider` → `config.GetSettingsManager().CustomProviders()`.
- `PUT /api/settings/provider/{name}` → decode body into
  `config.CustomProvider`, `SetCustomProvider(name, cfg)`, map
  `ErrInvalidCustomProvider` to 422.
- `DELETE /api/settings/provider/{name}` → 404 if not found (same
  existence check `handleDeleteMCPServer` does), else `DeleteCustomProvider`.

`GET /api/providers` (`handleProviders`) needs NO changes — it already
iterates `providers.All` generically; a custom provider shows up there
automatically once registered, with correct `credential_type: "api_key"`,
`is_subscription: false`, live `model_count`, etc.

Also documented in the hand-written OpenAPI spec (`server/server_docs.go`),
same shape as the MCP paths/schemas.

## CLI — `internal/cli`

New top-level command `harness provider` (singular — distinct from the
EXISTING `harness providers`, plural, which stays exactly as-is: the
read-only listing of every registered provider, built-in and custom
alike):

```go
type providerCmd struct {
	List providerListCmd `cmd:"" default:"1" help:"List custom providers"`
	Add  providerAddCmd  `cmd:"" help:"Add a custom OpenAI-compatible provider"`
	Rm   providerRmCmd   `cmd:"" aliases:"remove" help:"Remove a custom provider"`
}

type providerListCmd struct{}

type providerAddCmd struct {
	Name      string   `arg:"" help:"Provider name (must not collide with a built-in)"`
	Type      string   `default:"openai" help:"API dialect (only \"openai\" supported for now)"`
	URL       string   `required:"" help:"Base URL for chat completions"`
	ModelsURL string   `help:"Models listing URL (default: <url>/models)"`
	Header    []string `help:"HTTP header KEY:VAL (repeatable)"`
	Display   string   `help:"Human-friendly display name (default: the provider name)"`
	Disabled  bool     `help:"Add the provider disabled (default: enabled)"`
}

type providerRmCmd struct {
	Name string `arg:"" help:"Provider name"`
}
```

`RunProviderAdd`/`RunProviderList`/`RunProviderRm` in
`internal/cli/settings.go` (alongside the existing `RunMCPAdd`/etc.), thin
adapters calling the client's new `PUT`/`GET`/`DELETE
/api/settings/provider/...` methods — same shape as
`RunMCPAdd`/`RunMCPRm`. `--header KEY:VAL` parsed the same way
`RunMCPAdd`'s `--env`/`--header` flags already are (repeatable flag →
`map[string]string` via the existing `parseKV` helper in `kong_run.go`).

Connecting a custom provider (saving its API key) uses the ALREADY-GENERIC
`harness connect <name> <key>` — no new code needed there; `RunConnect`
already looks up the provider by name via `GET /api/providers` and drives
`Connect(creds)` generically for any `credential_type: "api_key"` result.

## What does NOT change

- `providers.Resolve`, `RunConnect`, `GET /api/providers`,
  `RefreshModels`/`RefreshProviderModels` — all already fully generic over
  `providers.All`, zero changes needed.
- `llm.DoOpenAIStream` / `llm.OpenAIRequest` — already accept
  `extraHeaders`; custom providers just pass their configured headers
  through the same parameter every other caller already uses.
- `llm.EnrichMeta`'s capability-enrichment cascade — unchanged, applied to
  custom-provider models exactly like every other provider.
- `OpenAI` (built-in) and its `isOpenAIChatModel` prefix filter — untouched.
  That filter stays correct and necessary for the REAL OpenAI API (whose
  `/v1/models` genuinely mixes chat/embedding/audio/image models with no
  capability field to distinguish them). It must never be reused for custom
  providers, whose models can have arbitrary names from arbitrary backends.
- MCP servers, scheduling, memory — untouched; this only adds a new keyed
  collection alongside the existing `mcp` one in `settings.json`.

## Testing

- `internal/config`: `validateCustomProvider` — accepts a well-formed
  `openai`-type entry, rejects unsupported `type`, rejects empty `url`,
  rejects a name colliding with each reserved built-in name.
  `SetCustomProvider`/`DeleteCustomProvider`/`CustomProviders` round-trip
  through a real file-backed `SettingsManager` (same pattern
  `settings_test.go` already uses for MCP).
- `internal/providers`: `CustomOpenAI.Name()`/`DisplayName()` (with and
  without an explicit `Display`), `ModelsURL` fallback to `<url>/models`,
  headers reaching both the chat-completions request AND the models-listing
  request (an `httptest.Server` capturing headers), and — the critical
  regression test — `fetchCustomOpenAIModels` accepting a model ID that
  would be REJECTED by `isOpenAIChatModel` (e.g. `"minimax-m3"`,
  `"llama-3.1-70b"`) to prove the OpenAI-specific filter never reaches this
  path.
- `internal/providers/registry.go`: a `Disabled: true` custom provider
  never appears in `initRegistry()`'s resulting `All`; a name colliding
  with a reserved built-in is skipped even if it somehow got past
  `SetCustomProvider`'s validation (defense in depth).
- `server`: `GET/PUT/DELETE /api/settings/provider/{name}` — same
  shape of tests as any existing settings endpoint (decode/validate/422
  mapping/404-on-missing-delete).
- CLI: `RunProviderAdd`/`RunProviderList`/`RunProviderRm` — thin, tested at
  the level `RunMCPAdd` already is (flag parsing → correct API payload).

## Design decisions worth flagging explicitly (Gus's calls during brainstorm)

- `Disabled: true` custom providers are excluded from the initial load —
  same behavior as MCP servers, confirmed explicitly.
- `Display` is optional, falls back to the map key (the provider name) —
  same fallback idiom as `ModelsURL` falling back to `<url>/models`.
- Model listing accepts ALL returned IDs with no capability-based
  filtering — investigated live whether OpenAI-compatible proxies expose a
  standard capabilities field in `/v1/models` (they don't; every
  implementation that adds one — LiteLLm, Lemonade, etc. — invents its own,
  non-standardized shape) and confirmed with Gus that unfiltered acceptance
  is correct, PROVIDED it is a genuinely separate code path from
  `isOpenAIChatModel` — explicitly re-confirmed after Gus flagged the exact
  failure mode (a MiniMax-model proxy returning 0 models if the OpenAI name
  prefix filter were ever applied to it).
- `type: "anthropic"` is explicitly out of scope for this iteration —
  `openai` is sufficient for now; revisit only if a real need for an
  Anthropic-dialect custom provider shows up (would require giving
  `Anthropic` a configurable `baseURL` first, which it doesn't have today).

## Addendum — programmatic (SDK) registration: `agent.NewOpenAIProvider`

Added in a follow-up round of the same brainstorm, once the declarative
(`settings.json`/`harness provider add`) path above was implemented and
working. The need: an SDK embedder wants to register a custom provider
directly in Go code (`main()`), without touching `settings.json` at all —
the same relationship `MINIMAX_API_KEY` (env var) already has to `harness
connect minimax <key>` (CLI/credentials.json), just for custom providers.

**Global, not per-Agent.** Investigated live whether `providers.All` is
per-process or per-`Agent`: it's a single package-level variable
(`var All = []Provider{}`), populated once via `sync.Once`
(`initOnce.Do(initRegistry)`), shared by every `Agent` in the process — this
is genuinely different from `mcp.Manager`, which IS per-`Agent` (a struct
field, `a.mcpManager`, each `Agent` with `EnableMCPs: true` gets its own).
Confirmed explicitly with Gus that global registration (matching how
`providers.All` already works, not inventing per-Agent isolation) is the
correct model — "el user en su main u otro lugar registra N providers,
luego ya crea el agente y pues todo lo demás le funciona".

**No `types.CustomProvider`/no interface exposed.** Two API shapes were
considered: (A) accept the same `types.CustomProvider` struct
programmatically, or (B) a public `Provider` interface the caller
implements themselves (full control, including their own auth/streaming).
Settled on neither exactly — Option A's shape, MINUS `ModelsURL` and
`Disabled` (neither makes sense for code-level registration: a disabled
provider is simply never registered, and "URL" the caller doesn't like is
replaced via the `FetchModels` hook below, not a second URL field), PLUS an
optional `FetchModels` override for exactly the one piece of per-provider
customization Gus wanted ("pero con la posibilidad e q implemneten el Fetch
model"). Option B (a fully open `Provider` interface) was explicitly
rejected — `internal/providers.Provider` is an 11-method interface designed
around the CLI's connect/disconnect/credential-persistence model; exposing
it (or a public subset) would either force SDK callers to implement
irrelevant methods or require maintaining a second, parallel provider
contract. A functional-options constructor over primitives is simpler and
sufficient for the stated need.

**Public surface (`agent/custom_providers.go`, aliased in `harness.go`):**

```go
func NewOpenAIProvider(name, url, apiKey string, opts ...CustomProviderOption) error

type CustomProviderOption func(*customProviderConfig) // customProviderConfig unexported — never leaked

func ProviderWithDisplay(display string) CustomProviderOption
func ProviderWithHeaders(headers map[string]string) CustomProviderOption
func ProviderWithFetchModels(fn func(apiKey string) ([]types.ModelMeta, error)) CustomProviderOption
```

No `internal/providers` type appears in any of these signatures — the
unexported `customProviderConfig` accumulator is mutated by
`CustomProviderOption` closures inside the `agent` package, then translated
to a `internal/providers.CustomOpenAI` construction inside
`NewOpenAIProvider`'s body only (legal: `agent` is in the same module,
already imports `internal/providers` directly elsewhere in `agent.go`).

**Credentials: memory-only, no `credentials.json` write.** `apiKey` is
passed directly into the constructed `CustomOpenAI` and held only in
memory — confirmed as the correct model by cross-checking how built-in
api-key providers ALREADY behave when activated via their environment
variable (`ANTHROPIC_API_KEY`, `MINIMAX_API_KEY`, …) instead of `harness
connect`: `resolveAPIKey`'s cascade checks the env var FIRST, falling back
to `credentials.json` only if unset — an env-var-activated built-in never
writes to `credentials.json` either. `RegisterOpenAI`'s memory-only apiKey
is the exact same pattern, just supplied as a function argument instead of
an environment variable.

**New `internal/providers.RegisterOpenAI`** — the real construction/
registration entry point `agent.NewOpenAIProvider` wraps:
```go
func RegisterOpenAI(name, url, apiKey, display string, headers map[string]string,
    fetchModels func(apiKey string) ([]types.ModelMeta, error)) error
```
Rejects a reserved built-in name (reusing `config.IsReservedProviderName`,
the same check `validateCustomProvider`/`buildCustomProviders` already use)
and a duplicate registration of the same name. Calls `EnsureRegistry()`
first (so the built-in + settings.json-configured providers are already in
`All` before appending), then appends under a new `registryMu sync.Mutex` —
added specifically because `All` previously had ZERO write protection
(only ever mutated once, inside `initOnce.Do`); `RegisterOpenAI` is a
SECOND, arbitrary-timing writer, so concurrent registration calls (or a
registration racing `initRegistry`'s own first-run construction) needed a
real lock. Reads throughout the codebase (`agent.go`, `server.go`,
`Resolve` itself) remain deliberately unprotected — the documented
contract is "register everything in `main()`, before constructing any
`Agent`", so registration finishes before any reader goroutine exists in
normal use; `registryMu` only needs to protect writers against each other.

**`CustomOpenAI.fetchModelsFn`** — new optional field, checked first in
`FetchModels()`; falls back to the existing `fetchCustomOpenAIModels` HTTP
path (unfiltered, per the addendum above) when nil. `types.CustomProvider`
(the settings.json-facing struct) does NOT gain this field — it has no
JSON representation, so a declaratively-configured provider always uses
the default HTTP discovery; only the programmatic path can supply custom
logic.

**Tests**: `internal/providers/register_openai_test.go` (registration
fields, reserved-name/duplicate-name rejection, the `FetchModels` hook
actually being called instead of the default HTTP path — proven by a
server that fails the test if hit, `Resolve("name/model")` working
end-to-end), `agent/custom_providers_test.go` (the same coverage through
`agent.NewOpenAIProvider` and a real `*Agent`), `harness_test.go`'s
`TestNewOpenAIProviderFacadeAliasIsWired` (the facade aliases genuinely
reach `agent.NewOpenAIProvider`, not just type-check). All three test files
isolate `HOME` via `t.Setenv` (config.GetSettingsManager()'s singleton
caveat — see `server/create_session_thinking_test.go`) and
snapshot/restore `providers.All` so registrations never leak across tests
in the same binary.
