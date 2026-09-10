# WebSearch tool — design

## Goal

A new built-in tool, `WebSearch`, that performs `POST /v1/coding_plan/search`
against MiniMax's coding-plan API and returns the result list to the model.
This unblocks agents operating against rates/time-sensitive info that is
outside their training cutoff or session memory.

## Activation

- New `AgentOptions.EnableWebSearch bool` flag.
- The tool is registered in `Agent.buildSessionTools` whenever the flag is
  true. There is no pre-flight lookup of the `minimax` provider.
- If the model calls `WebSearch` and the `minimax` provider is inactive,
  `Execute` returns a single, actionable error: `"WebSearch requires the
  'minimax' provider to be connected (run 'harness connect minimax' or set
  MINIMAX_API_KEY)"`.
- The flag defaults to `true` for every transport agent (TUI, Telegram,
  Slack, `harness serve`, ACP). One-shots that don't want it can opt out
  explicitly. Subagents inherit it.

## Provider integration — none

The tool is fully self-contained:

- Owns its own `http.Client` (constructor injects it).
- Owns the URL constant, the headers, the JSON shape, the response parser.
- Does NOT call into `internal/providers/minimax.go` for a `Search()`
  helper. That file stays single-purpose: the chat completion provider.
- Decoupling from the provider registry is achieved via a small
  `ProviderLookup` interface (IsActive/ResolveCredentials) injected by
  the agent. The agent resolves the right `minimax` provider instance via
  `providers.EnsureRegistry` and wraps the minimal facade.

## Tool shape

`agent/tools/websearch.go`:

```go
type searchInput struct {
    Query   string `json:"query"`
    Limit   int    `json:"limit,omitempty"`
    Timeout int    `json:"timeout,omitempty"`
}

func WebSearch(lookup ProviderLookup, client *http.Client) Tool
```

JSON Schema for the model:

- `query` — string, required. 3-5 keywords; doc encourages a date when
  the topic is time-sensitive.
- `limit` — int, optional, default 10, clamped to `[1, 50]`.
- `timeout` — int (seconds), optional, default 30, clamped to `>=1`.

Validation:

- Empty `query` → tool error `"query is required"`.
- `limit <= 0` → 10; `limit > 50` → 50.
- `timeout <= 0` → 30.

## HTTP contract

- URL: `POST https://api.minimax.io/v1/coding_plan/search`.
- Headers:
  - `Content-Type: application/json`.
  - `Authorization: Bearer <api_key>` — obtained fresh each call via
    `ProviderLookup.ResolveCredentials()` so a credential refresh
    immediately affects subsequent searches.
  - `MM-API-Source: WebSearch-MCP` (matches the upstream client's
    `MM-API-Source` value referenced by `MiniMax-Coding-Plan-MCP`).
- Body: `{"q": "<query>"}`.

## Response handling

The endpoint returns:

```json
{
  "organic": [
    {"title": "...", "link": "...", "snippet": "...", "date": "..."}
  ],
  "related_searches": [...],
  "base_resp": {"status_code": 0, "status_msg": "..."}
}
```

- Parse into a local struct (no leaking into `types`).
- Truncate `organic` to `limit` entries.
- Return the slice as standard pretty-printed JSON via `json.MarshalIndent`.
  No custom formatting. Empty slice returns `[]`.
- `base_resp.status_code != 0`:
  - Map 1004 / 2049 to actionable messages
    (the auth/key check, the real-name check).
  - Otherwise surface `base_resp.status_code` + `base_resp.status_msg`.

## Errors

| Condition                                      | Output                                                 |
| ---------------------------------------------- | ------------------------------------------------------ |
| `minimax` provider inactive or empty key       | "WebSearch requires the 'minimax' provider..."         |
| HTTP non-2xx                                   | `types.NewProviderAPIError("minimax", status, body)`   |
| `base_resp.status_code != 0` (e.g. 2049)       | Actionable message; not a hard "API error"               |
| Timeout / network blip                         | `fmt.Errorf("web search: %w", err)`                    |
| Empty `organic` array                          | Return `[]` — a legitimate empty result, not an error  |

No raw bodies / headers are included in returned strings; no API key ever
appears in the tool output.

## Rendering (TUI)

- Own icon: **🔍** (magnifying glass).
- `query` is the primary argument and is rendered as bare text after the
  icon (NOT as `key=value`). Mirrors how the user expects "search"
  output to read.
- `limit`, `timeout` render as standard `key=value` like every other
  built-in tool arg.
- Output renders as pretty JSON with the standard `ResultBlock`.

## Touchpoints

| File                                                | Change                                         |
| --------------------------------------------------- | ---------------------------------------------- |
| `agent/tools/websearch.go`                          | NEW — tool + `searchInput` + helpers           |
| `agent/tools/websearch_test.go`                     | NEW — httptest-based unit tests                 |
| `agent/tools/names.go`                              | `+ToolWebSearch = "WebSearch"` constant         |
| `agent/agent.go`                                    | `+AgentOptions.EnableWebSearch`, registration  |
| `agent/tools/fetch.go`                              | Possibly pass `*http.Client` factory pattern    |
| `internal/cli/agent.go`                             | `EnableWebSearch: true` for interactive helper  |
| `internal/cli/kong_run_telegram.go`                 | `EnableWebSearch: true`                         |
| `internal/cli/kong_run_slack.go`                   | `EnableWebSearch: true`                         |
| `internal/cli/kong_run_serve.go`                   | `EnableWebSearch: true`                         |
| `internal/cli/kong_run_acp.go`                     | `EnableWebSearch: true`                         |
| `internal/tui/toolfmt.go`                           | 🔍 icon + primary-arg rendering                |

## What does NOT change

- `internal/providers/minimax.go` — untouched. The new tool is the only
  HTTP client speaking the search dialect.
- The chat-completion code path.
- Credentials storage.

## Tests

Unit (using `httptest.NewServer`):

1. 200 with 3 organic entries, `limit=2` → 2 entries returned.
2. 200 without `organic` → `[]`.
3. 401 → `ProviderAPIError{status=401}`.
4. 403 → `ProviderAPIError{status=403}`.
5. 500 → `ProviderAPIError{status=500}`.
6. `base_resp.status_code=2049` → error string mentions the key.
7. Server delay > timeout → ctx.Err wrapped.
8. `query=""` → tool error.
9. `limit=99` → server receives a sliced-to-50 organic (mock and assert by
   limiting the local side after parse: count assertions only).
10. Provider inactive stub → actionable connect message; no network traffic.

All existing tests continue to pass (`go test ./...`) and `go vet ./...`
clean.
