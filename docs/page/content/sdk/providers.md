---
title: Custom providers
section: SDK
order: 330
summary: Register your own OpenAI-compatible or Anthropic-compatible endpoint, declaratively or from Go, and share a subscription between Harness instances.
---

A custom provider is an endpoint speaking one of two dialects that Harness uses like a built-in provider, as `name/model`:

- `openai`: OpenAI Chat Completions (a proxy, gateway or self-hosted server)
- `anthropic`: Anthropic Messages (Anthropic itself, a gateway, or another Harness instance's provider proxy)

A custom provider has **no credential of its own**. `harness connect` is rejected for it and it is always reported active. Any authentication is carried as static request headers (`Authorization: Bearer ...`, `X-Api-Key: ...`, or several), sent on every request including model discovery.

There are two registration paths. Both end up in the same registry.

## Declarative: settings

```bash
$ harness provider add my-gateway --url https://llm.example.com/v1 --header X-Api-Key:<key>
$ harness provider add shared-claude --type anthropic --url http://10.0.0.5:8080/api/providers/claude-oauth/proxy
$ harness provider rm my-gateway
```

Options of `provider add`: `--type openai|anthropic` (default `openai`), `--url` (required), `--models-url`, `--header KEY:VAL` (repeatable), `--display`, `--disabled`, `--reasoning-split`.

The entries live in settings under `provider` and are stored as `types.CustomProvider`:

| Field | Meaning |
|---|---|
| `type` | `openai` or `anthropic` |
| `url` | `openai`: base URL for chat completions, including any `/v1`. `anthropic`: API root **without** `/v1` (`/v1/messages` and `/v1/models` are appended). |
| `models_url` | Optional model listing URL. Defaults to `<url>/models` (openai) or `<url>/v1/models` (anthropic). |
| `headers` | Extra HTTP headers; where any authentication goes. |
| `display` | Optional display name. |
| `disabled` | Skip the entry. |
| `reasoning_split` | `openai` only; see [below](#reasoning-split). |

Entries are read at process start. To apply changes to a running process, call:

```go
err := harness.ReloadCustomProviders()
```

Added, edited, disabled and deleted entries take effect without a restart. Built-in providers and providers registered from code are untouched. Running sessions pick the change up at their next turn; a turn in flight finishes with the provider it started with. It is idempotent: unchanged entries keep their provider and model cache. If an entry's name is already used by a provider registered from code, the programmatic one wins, the entry is skipped, and the returned error wraps `harness.ErrProviderNameConflict` (the rest is still applied).

Over HTTP, the same is `PUT`/`DELETE /api/settings/provider/{name}` followed by `POST /api/settings/provider/reload` (`client.PutCustomProvider`, `DeleteCustomProvider`, `ReloadCustomProviders`).

## Programmatic: Go

```go
err := harness.NewOpenAIProvider("my-gateway", "https://llm.example.com/v1",
	harness.ProviderWithDisplay("Acme Gateway"),
	harness.ProviderWithHeaders(map[string]string{"X-Api-Key": apiKey}),
)
if err != nil {
	log.Fatal(err)
}

sess, err := a.NewSession(cwd, "my-gateway/some-model")
```

`NewAnthropicProvider(name, url, opts...)` is the same for the Messages dialect. Its `url` is the API root **without** `/v1`.

Registration is global to the process and takes effect for any session resolved afterwards. It may be called at any time, even while agents run. The name must not collide with a built-in provider (`anthropic`, `openai`, `minimax`, ...) or an existing registration; those return an error and register nothing.

| Option | Meaning |
|---|---|
| `ProviderWithDisplay(name)` | Display name in listings. Defaults to the provider name. |
| `ProviderWithHeaders(map)` | Headers sent on every request, chat and model listing. All authentication goes here. |
| `ProviderWithFetchModels(fn)` | Replaces model discovery entirely. `fn func() ([]types.ModelMeta, error)` takes no argument; capture any credentials in its closure. |
| `ProviderWithReasoningSplit()` | Sends `"reasoning_split": true`. OpenAI dialect only; ignored for `anthropic`. |

Default discovery is `GET <url>/models`, parsed as `{"data": [{"id": "..."}]}`, accepting every returned id.

### Replacing and removing

```go
err := harness.UnregisterCustomProvider("my-gateway")
// register again with a new key or tenant
```

Running sessions adopt a provider registered again under the same name at their next turn. If none is registered by then, the turn fails with an error wrapping `harness.ErrProviderNotFound`.

`UnregisterCustomProvider` only removes providers registered from code. For a built-in provider or one declared in settings it returns an error wrapping `harness.ErrProviderNotRemovable`; manage settings entries with `harness provider rm` or the settings API.

Test these with `errors.Is`.

> **Note** Registrations are process-global. In tests, unregister whatever you registered in a cleanup function so one test does not change which providers a later one sees.

## Reasoning split

Some MiniMax-compatible backends emit their thinking inline in `content`, wrapped in literal `<think>...</think>`, unless the request carries `"reasoning_split": true`. With the flag, thinking arrives in the separate `reasoning_content` field, which Harness streams as thinking events.

Enable it only for such a backend: `--reasoning-split` on the CLI, `reasoning_split: true` in settings, or `ProviderWithReasoningSplit()` in Go. Leave it off otherwise; stricter OpenAI-compatible APIs reject the unknown field.

## Sharing a subscription

A running Harness server proxies the native APIs of its providers at `/api/providers/{name}/proxy/...`, supplying its own credentials. A second Harness instance can use the first one's subscription as a provider with no credential of its own:

```go
err := harness.NewAnthropicProvider("shared-claude",
	"http://10.0.0.5:8080/api/providers/claude-oauth/proxy")
```

Or from the CLI:

```bash
$ harness provider add shared-claude --type anthropic --url http://10.0.0.5:8080/api/providers/claude-oauth/proxy
```

The custom provider sends plain Messages requests. The proxy applies the identity and tool-name mapping that the subscription requires, once. If the serving instance uses `--secret`, the proxy requires it on every request: pass it as a header with `ProviderWithHeaders(map[string]string{"X-Harness-Sec": secret})`.

The dialect type `openai-responses`, which would share a Codex subscription the same way, is not supported as a custom provider type.

To call a provider's native API through a server from code, use `client.ProviderBaseURL` and `ProviderDo` (see [Client](client.md#provider-proxy)). The proxy is described in [HTTP/SSE server](../frontends/server.md).
