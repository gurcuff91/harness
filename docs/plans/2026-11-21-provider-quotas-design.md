# Provider quotas — design

Status: approved by the owner (2026-11-21).

## Goal

`GET /api/providers/{name}/quota` returns the subscription usage of a provider
in ONE format, whatever its own API looks like.

## Findings (verified live 2026-11-21 unless noted)

| Provider | Endpoint | Shape |
|---|---|---|
| claude-oauth | `GET https://api.anthropic.com/api/oauth/usage` (Bearer) | `limits[]`: kind/group/percent (0–100)/resets_at (ISO)/scope.model.display_name; also five_hour/seven_day. 429 if polled hard |
| codex-oauth | `GET https://chatgpt.com/backend-api/wham/usage` (Bearer + ChatGPT-Account-ID) | `plan_type`, `rate_limit.{primary,secondary}_window.{used_percent, limit_window_seconds, reset_at (epoch s)}`, `additional_rate_limits` |
| minimax | `GET https://api.minimax.io/v1/token_plan/remains` (Bearer) | `model_remains[]` per bucket (general, video): `current_interval_remaining_percent`, `end_time` (ms), `current_weekly_remaining_percent`, `weekly_end_time` (ms). HTTP 200 even on error: check `base_resp.status_code` |
| synthetic | `GET https://api.synthetic.new/v2/quotas` (Bearer) | `planWeeklyLimit.{percentRemaining, nextRegenAt}`, `search.hourly.{limit,requests,renewsAt}`, `subscription.{limit,requests,renewsAt}` |
| opencode-go | `GET https://opencode.ai/zen/go/v1/usage` (Bearer [+ x-opencode-org-id]) | `usage.{rolling,weekly,monthly}.{status, percent (used), resetsAt}` — route confirmed (401), shape from source, the stored key is expired |
| ollama-cloud | `GET https://ollama.com/api/balance` (Bearer), plan from `POST /api/me` | current plans: `included.{balance_usd, allowance_usd, period.until}`, `purchased.balance_usd`; legacy: `included.{session,weekly}.{remaining_percent, resets_at}`. 10 req/min |

## Design

1. **Optional interface** in `internal/providers`: `QuotaReporter { Quota(ctx) (*types.Quota, error) }`,
   implemented by the six providers above. `Provider` is unchanged. Each uses
   its own credentials (OAuth refreshed as usual) and client.
2. **One format** (`types.Quota`): `provider`, `plan?`, `fetched_at`, `stale?`,
   `windows[]` (`name`, `label`, `scope?`, `used_percent` 0–100, `resets_at` ISO
   UTC), `balance?` (`remaining_usd`, `allowance_usd`, `resets_at`). Providers
   that report what REMAINS are converted (`100 - remaining`); every time format
   becomes ISO UTC.
3. **API**: `GET /api/providers/{name}/quota[?refresh=true]`. 404 no such
   provider / no quota, 409 not connected, 502 provider failed with no earlier
   reading. `client.ProviderQuota`. OpenAPI.
4. **Cache** (in memory, per provider): 60 s; `refresh` forces a new reading but
   never more than one per 5 s; a failure (429 included) with an earlier good
   reading returns it with `stale: true`, honoring `Retry-After`.
5. **Fixes in the same release**: codex-oauth recovers a missing `account_id`
   from the access token's claim (and stores it); an api-key provider whose key
   is rejected reports it in `/api/providers` `error` instead of a silent
   "0 models".

TUI: `/info` shows the quota of the session's provider (added in 0.113.0, option A — the session's provider only).
