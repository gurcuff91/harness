package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

// QuotaReporter is implemented by the providers that can report their
// subscription usage (GET /api/providers/{name}/quota). Optional: Provider is
// unchanged, and a provider without it simply has no quota.
//
// Quota reads the provider's own usage endpoint with the provider's own
// credentials (an OAuth token refreshed as usual) and returns it in the one
// format of types.Quota. It does no caching (the server does). A provider that
// is not connected returns an error wrapping ErrNoUpstream; one whose account
// has no quota to report returns ErrNoQuota; a failed HTTP call returns a
// *QuotaHTTPError.
type QuotaReporter interface {
	Quota(ctx context.Context) (*types.Quota, error)
}

var (
	_ QuotaReporter = (*ClaudeOAuth)(nil)
	_ QuotaReporter = (*CodexOAuth)(nil)
	_ QuotaReporter = (*MiniMax)(nil)
	_ QuotaReporter = (*Synthetic)(nil)
	_ QuotaReporter = (*OpenCodeGo)(nil)
	_ QuotaReporter = (*OllamaCloud)(nil)
)

// ErrNoQuota means the provider is connected but its account has nothing to
// report (opencode-go without a Go subscription).
var ErrNoQuota = errors.New("no quota for this account")

// QuotaHTTPError is a usage endpoint answering something other than 200.
type QuotaHTTPError struct {
	Provider string
	Status   int
	// RetryAfter is the provider's Retry-After, zero when it sent none.
	RetryAfter time.Duration
	// Reason is a short reason from the body when it had one.
	Reason string
}

func (e *QuotaHTTPError) Error() string {
	msg := fmt.Sprintf("%s usage endpoint: HTTP %d", e.Provider, e.Status)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// quotaTimeout bounds one usage call (the provider clients have no total
// timeout: they are streaming clients).
const quotaTimeout = 15 * time.Second

// Usage endpoints. Variables so a test can point them at a fake server.
var (
	claudeUsageURL    = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL     = "https://chatgpt.com/backend-api/wham/usage"
	minimaxQuotaURL   = "https://api.minimax.io/v1/token_plan/remains"
	openCodeGoUsage   = "https://opencode.ai/zen/go/v1/usage"
	ollamaBalanceURL  = "https://ollama.com/api/balance"
	ollamaMeURL       = "https://ollama.com/api/me"
	syntheticQuotaURL = syntheticQuotasURL
)

// fetchQuotaJSON calls a usage endpoint with the provider's Upstream headers
// (the same credentials CompleteStream sends) on its own client, and returns
// the 200 body. Any other status is a *QuotaHTTPError.
func fetchQuotaJSON(ctx context.Context, provider string, up Upstream, method, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	client := up.Client
	if client == nil {
		client = modelListClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s usage endpoint unreachable: %w", provider, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s usage endpoint: reading body: %w", provider, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &QuotaHTTPError{
			Provider:   provider,
			Status:     resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			Reason:     keyRejectionReason(body),
		}
	}
	return body, nil
}

// parseRetryAfter reads a Retry-After header: delta seconds or an HTTP date.
// Zero when absent, malformed or already past.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// ── Normalisation helpers ────────────────────────────────────────────────

// clampPercent keeps a percentage in 0–100, rounded to two decimals: what a
// provider reports as remaining (Synthetic's 99.99999476…) would otherwise come
// out as float noise (0.0000052…).
func clampPercent(p float64) float64 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	}
	return math.Round(p*100) / 100
}

// usedFromRemaining converts a "remaining" percentage into "used".
func usedFromRemaining(remaining float64) float64 { return clampPercent(100 - remaining) }

// parseQuotaTime reads an ISO-8601 time (with any offset) as UTC; zero when
// empty or malformed.
func parseQuotaTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// unixSeconds / unixMillis convert an epoch to UTC; zero for 0.
func unixSeconds(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

func unixMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// periodLabel names a window length: "5-hour", "7-day", "30-day", "90-minute".
func periodLabel(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d-day", int(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d-hour", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d-minute", int(d.Round(time.Minute)/time.Minute))
	}
}

func newQuota(provider string, now time.Time) *types.Quota {
	return &types.Quota{Provider: provider, FetchedAt: now.UTC(), Windows: []types.QuotaWindow{}}
}

// ── claude-oauth ─────────────────────────────────────────────────────────

// Quota implements QuotaReporter: GET /api/oauth/usage with the subscription's
// OAuth token. Polled hard it answers 429 (the server caches).
func (c *ClaudeOAuth) Quota(ctx context.Context) (*types.Quota, error) {
	up, err := c.Upstream()
	if err != nil {
		return nil, err
	}
	body, err := fetchQuotaJSON(ctx, c.Name(), up, http.MethodGet, claudeUsageURL)
	if err != nil {
		return nil, err
	}
	return parseClaudeQuota(body, time.Now())
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

// parseClaudeQuota reads the usage answer. limits[] is the complete list
// (session, weekly_all, weekly_scoped per model family); five_hour/seven_day
// are the older summary, used only when limits[] is absent.
func parseClaudeQuota(body []byte, now time.Time) (*types.Quota, error) {
	var v struct {
		Limits []struct {
			Kind     string  `json:"kind"`
			Group    string  `json:"group"`
			Percent  float64 `json:"percent"`
			ResetsAt string  `json:"resets_at"`
			Scope    *struct {
				Model *struct {
					DisplayName string `json:"display_name"`
				} `json:"model"`
			} `json:"scope"`
		} `json:"limits"`
		FiveHour *claudeUsageWindow `json:"five_hour"`
		SevenDay *claudeUsageWindow `json:"seven_day"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("claude-oauth usage: %w", err)
	}
	q := newQuota("claude-oauth", now)
	for _, l := range v.Limits {
		scope := ""
		if l.Scope != nil && l.Scope.Model != nil {
			scope = l.Scope.Model.DisplayName
		}
		w := types.QuotaWindow{UsedPercent: clampPercent(l.Percent), ResetsAt: parseQuotaTime(l.ResetsAt), Scope: scope}
		switch {
		case l.Kind == "session":
			w.Name, w.Label = "session", "5-hour session"
		case l.Kind == "weekly_all":
			w.Name, w.Label = "weekly", "Weekly (all models)"
		case scope != "":
			group := l.Group
			if group == "" {
				group = l.Kind
			}
			if group == "" {
				group = "limit"
			}
			w.Name = group + ":" + scope
			w.Label = strings.ToUpper(group[:1]) + group[1:] + " (" + scope + ")"
		default:
			w.Name, w.Label = l.Kind, l.Kind
		}
		q.Windows = append(q.Windows, w)
	}
	if len(v.Limits) == 0 {
		if w := v.FiveHour; w != nil && w.Utilization != nil {
			q.Windows = append(q.Windows, types.QuotaWindow{Name: "session", Label: "5-hour session", UsedPercent: clampPercent(*w.Utilization), ResetsAt: parseQuotaTime(w.ResetsAt)})
		}
		if w := v.SevenDay; w != nil && w.Utilization != nil {
			q.Windows = append(q.Windows, types.QuotaWindow{Name: "weekly", Label: "Weekly (all models)", UsedPercent: clampPercent(*w.Utilization), ResetsAt: parseQuotaTime(w.ResetsAt)})
		}
	}
	return q, nil
}

// ── codex-oauth ──────────────────────────────────────────────────────────

// Quota implements QuotaReporter: GET /backend-api/wham/usage with the ChatGPT
// token and ChatGPT-Account-ID (both from Upstream).
func (c *CodexOAuth) Quota(ctx context.Context) (*types.Quota, error) {
	up, err := c.Upstream()
	if err != nil {
		return nil, err
	}
	body, err := fetchQuotaJSON(ctx, c.Name(), up, http.MethodGet, codexUsageURL)
	if err != nil {
		return nil, err
	}
	return parseCodexQuota(body, time.Now())
}

type codexRateWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type codexRateLimit struct {
	PrimaryWindow   *codexRateWindow `json:"primary_window"`
	SecondaryWindow *codexRateWindow `json:"secondary_window"`
}

// parseCodexQuota reads the usage answer: rate_limit's primary and secondary
// windows (either may be null — a free plan has one 30-day window), plus
// additional_rate_limits, each scoped to its limit_name.
func parseCodexQuota(body []byte, now time.Time) (*types.Quota, error) {
	var v struct {
		PlanType             string          `json:"plan_type"`
		RateLimit            *codexRateLimit `json:"rate_limit"`
		AdditionalRateLimits []struct {
			LimitName      string          `json:"limit_name"`
			MeteredFeature string          `json:"metered_feature"`
			RateLimit      *codexRateLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("codex-oauth usage: %w", err)
	}
	q := newQuota("codex-oauth", now)
	q.Plan = v.PlanType
	add := func(prefix, scope string, rl *codexRateLimit) {
		if rl == nil {
			return
		}
		for i, w := range []*codexRateWindow{rl.PrimaryWindow, rl.SecondaryWindow} {
			if w == nil {
				continue
			}
			kind := [...]string{"primary", "secondary"}[i]
			label := periodLabel(time.Duration(w.LimitWindowSeconds) * time.Second)
			if label == "" {
				label = kind
			}
			name := kind
			if prefix != "" {
				name = prefix + ":" + kind
				label += " (" + scope + ")"
			}
			q.Windows = append(q.Windows, types.QuotaWindow{
				Name: name, Label: label, Scope: scope,
				UsedPercent: clampPercent(w.UsedPercent), ResetsAt: unixSeconds(w.ResetAt),
			})
		}
	}
	add("", "", v.RateLimit)
	for _, a := range v.AdditionalRateLimits {
		name := a.LimitName
		if name == "" {
			name = a.MeteredFeature
		}
		add(name, name, a.RateLimit)
	}
	return q, nil
}

// ── minimax ──────────────────────────────────────────────────────────────

// Quota implements QuotaReporter: GET /v1/token_plan/remains (Token Plan).
func (o *MiniMax) Quota(ctx context.Context) (*types.Quota, error) {
	up, err := o.Upstream()
	if err != nil {
		return nil, err
	}
	body, err := fetchQuotaJSON(ctx, o.Name(), up, http.MethodGet, minimaxQuotaURL)
	if err != nil {
		return nil, err
	}
	return parseMiniMaxQuota(body, time.Now())
}

// parseMiniMaxQuota reads model_remains: one entry per bucket ("general" for
// text, "video", …), each with an interval window (5 h for text, a day for
// video) and a weekly one, reported as what REMAINS. MiniMax answers HTTP 200
// even for an error: base_resp.status_code must be 0.
func parseMiniMaxQuota(body []byte, now time.Time) (*types.Quota, error) {
	var v struct {
		ModelRemains []struct {
			ModelName                       string   `json:"model_name"`
			StartTime                       int64    `json:"start_time"`
			EndTime                         int64    `json:"end_time"`
			CurrentIntervalRemainingPercent *float64 `json:"current_interval_remaining_percent"`
			WeeklyStartTime                 int64    `json:"weekly_start_time"`
			WeeklyEndTime                   int64    `json:"weekly_end_time"`
			CurrentWeeklyRemainingPercent   *float64 `json:"current_weekly_remaining_percent"`
		} `json:"model_remains"`
		BaseResp *struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("minimax usage: %w", err)
	}
	if v.BaseResp != nil && v.BaseResp.StatusCode != 0 {
		return nil, fmt.Errorf("minimax usage: %s (status %d)", v.BaseResp.StatusMsg, v.BaseResp.StatusCode)
	}
	q := newQuota("minimax", now)
	for _, m := range v.ModelRemains {
		bucket := m.ModelName
		if p := m.CurrentIntervalRemainingPercent; p != nil {
			label := periodLabel(time.Duration(m.EndTime-m.StartTime) * time.Millisecond)
			if label == "" {
				label = "interval"
			}
			q.Windows = append(q.Windows, types.QuotaWindow{
				Name: bucket + ":interval", Label: label + " (" + bucket + ")", Scope: bucket,
				UsedPercent: usedFromRemaining(*p), ResetsAt: unixMillis(m.EndTime),
			})
		}
		if p := m.CurrentWeeklyRemainingPercent; p != nil {
			q.Windows = append(q.Windows, types.QuotaWindow{
				Name: bucket + ":weekly", Label: "Weekly (" + bucket + ")", Scope: bucket,
				UsedPercent: usedFromRemaining(*p), ResetsAt: unixMillis(m.WeeklyEndTime),
			})
		}
	}
	return q, nil
}

// ── synthetic ────────────────────────────────────────────────────────────

// Quota implements QuotaReporter: GET /v2/quotas.
func (o *Synthetic) Quota(ctx context.Context) (*types.Quota, error) {
	up, err := o.Upstream()
	if err != nil {
		return nil, err
	}
	body, err := fetchQuotaJSON(ctx, o.Name(), up, http.MethodGet, syntheticQuotaURL)
	if err != nil {
		return nil, err
	}
	return parseSyntheticQuota(body, time.Now())
}

type syntheticCounter struct {
	Limit    float64 `json:"limit"`
	Requests float64 `json:"requests"`
	RenewsAt string  `json:"renewsAt"`
}

func (c *syntheticCounter) used() float64 {
	if c.Limit <= 0 {
		return 0
	}
	return clampPercent(c.Requests / c.Limit * 100)
}

// parseSyntheticQuota reads /v2/quotas: subscription (request count over the
// plan's window), planWeeklyLimit (a rolling weekly budget reported as what
// remains; it regenerates a little at nextRegenAt, which is therefore its
// resets_at) and search.hourly. A credits-only account answers {}: no windows.
func parseSyntheticQuota(body []byte, now time.Time) (*types.Quota, error) {
	var v struct {
		Subscription    *syntheticCounter `json:"subscription"`
		PlanWeeklyLimit *struct {
			PercentRemaining *float64 `json:"percentRemaining"`
			NextRegenAt      string   `json:"nextRegenAt"`
		} `json:"planWeeklyLimit"`
		Search *struct {
			Hourly *syntheticCounter `json:"hourly"`
		} `json:"search"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("synthetic usage: %w", err)
	}
	q := newQuota("synthetic", now)
	if s := v.Subscription; s != nil {
		q.Windows = append(q.Windows, types.QuotaWindow{Name: "requests", Label: "Subscription requests", UsedPercent: s.used(), ResetsAt: parseQuotaTime(s.RenewsAt)})
	}
	if w := v.PlanWeeklyLimit; w != nil && w.PercentRemaining != nil {
		q.Windows = append(q.Windows, types.QuotaWindow{Name: "weekly", Label: "Weekly (rolling)", UsedPercent: usedFromRemaining(*w.PercentRemaining), ResetsAt: parseQuotaTime(w.NextRegenAt)})
	}
	if v.Search != nil && v.Search.Hourly != nil {
		h := v.Search.Hourly
		q.Windows = append(q.Windows, types.QuotaWindow{Name: "search:hourly", Label: "Hourly (search)", Scope: "search", UsedPercent: h.used(), ResetsAt: parseQuotaTime(h.RenewsAt)})
	}
	return q, nil
}

// ── opencode-go ──────────────────────────────────────────────────────────

// Quota implements QuotaReporter: GET /zen/go/v1/usage. A 403 means the key
// has no Go subscription (opencode's own client reads it so): ErrNoQuota.
func (o *OpenCodeGo) Quota(ctx context.Context) (*types.Quota, error) {
	up, err := o.Upstream()
	if err != nil {
		return nil, err
	}
	body, err := fetchQuotaJSON(ctx, o.Name(), up, http.MethodGet, openCodeGoUsage)
	var he *QuotaHTTPError
	if errors.As(err, &he) && he.Status == http.StatusForbidden {
		return nil, ErrNoQuota
	}
	if err != nil {
		return nil, err
	}
	return parseOpenCodeGoQuota(body, time.Now())
}

// parseOpenCodeGoQuota reads usage.{rolling,weekly,monthly}: percent is what
// is USED (0–100), resetsAt ISO. Shape from opencode's own client
// (packages/core/src/usage-limits.ts), not seen live.
func parseOpenCodeGoQuota(body []byte, now time.Time) (*types.Quota, error) {
	type meter struct {
		Percent  float64 `json:"percent"`
		ResetsAt string  `json:"resetsAt"`
	}
	var v struct {
		Usage struct {
			Rolling *meter `json:"rolling"`
			Weekly  *meter `json:"weekly"`
			Monthly *meter `json:"monthly"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("opencode-go usage: %w", err)
	}
	q := newQuota("opencode-go", now)
	for _, m := range []struct {
		name, label string
		m           *meter
	}{{"rolling", "Rolling", v.Usage.Rolling}, {"weekly", "Weekly", v.Usage.Weekly}, {"monthly", "Monthly", v.Usage.Monthly}} {
		if m.m != nil {
			q.Windows = append(q.Windows, types.QuotaWindow{Name: m.name, Label: m.label, UsedPercent: clampPercent(m.m.Percent), ResetsAt: parseQuotaTime(m.m.ResetsAt)})
		}
	}
	return q, nil
}

// ── ollama-cloud ─────────────────────────────────────────────────────────

// Quota implements QuotaReporter: GET /api/balance, and the plan name from
// POST /api/me (best effort: a failure there only leaves the plan empty).
// Ollama allows about 10 of these calls a minute (the server caches).
func (o *OllamaCloud) Quota(ctx context.Context) (*types.Quota, error) {
	up, err := o.Upstream()
	if err != nil {
		return nil, err
	}
	body, err := fetchQuotaJSON(ctx, o.Name(), up, http.MethodGet, ollamaBalanceURL)
	if err != nil {
		return nil, err
	}
	q, err := parseOllamaQuota(body, time.Now())
	if err != nil {
		return nil, err
	}
	if me, err := fetchQuotaJSON(ctx, o.Name(), up, http.MethodPost, ollamaMeURL); err == nil {
		var v struct {
			Plan string `json:"Plan"`
		}
		if json.Unmarshal(me, &v) == nil {
			q.Plan = v.Plan
		}
	}
	return q, nil
}

// parseOllamaQuota reads /api/balance. Current plans meter dollars:
// included.{balance_usd, allowance_usd, period.until} plus purchased.balance_usd
// → Balance, and a window for the included allowance when there is one.
// Legacy plans report included.{session,weekly}.{remaining_percent,resets_at}.
func parseOllamaQuota(body []byte, now time.Time) (*types.Quota, error) {
	type legacy struct {
		RemainingPercent *float64 `json:"remaining_percent"`
		ResetsAt         string   `json:"resets_at"`
	}
	var v struct {
		Included *struct {
			BalanceUSD   *float64 `json:"balance_usd"`
			AllowanceUSD *float64 `json:"allowance_usd"`
			Period       *struct {
				Until string `json:"until"`
			} `json:"period"`
			Session *legacy `json:"session"`
			Weekly  *legacy `json:"weekly"`
		} `json:"included"`
		Purchased *struct {
			BalanceUSD float64 `json:"balance_usd"`
		} `json:"purchased"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("ollama-cloud usage: %w", err)
	}
	q := newQuota("ollama-cloud", now)
	inc := v.Included
	if inc == nil {
		return q, nil
	}
	if inc.BalanceUSD != nil || inc.AllowanceUSD != nil {
		b := &types.QuotaBalance{}
		if inc.BalanceUSD != nil {
			b.RemainingUSD = *inc.BalanceUSD
		}
		if v.Purchased != nil {
			b.RemainingUSD += v.Purchased.BalanceUSD
		}
		if inc.AllowanceUSD != nil {
			b.AllowanceUSD = *inc.AllowanceUSD
		}
		if inc.Period != nil {
			b.ResetsAt = parseQuotaTime(inc.Period.Until)
		}
		q.Balance = b
		if b.AllowanceUSD > 0 && inc.BalanceUSD != nil {
			q.Windows = append(q.Windows, types.QuotaWindow{
				Name: "allowance", Label: "Included allowance",
				UsedPercent: clampPercent((b.AllowanceUSD - *inc.BalanceUSD) / b.AllowanceUSD * 100),
				ResetsAt:    b.ResetsAt,
			})
		}
	}
	for _, l := range []struct {
		name, label string
		w           *legacy
	}{{"session", "Session", inc.Session}, {"weekly", "Weekly", inc.Weekly}} {
		if l.w != nil && l.w.RemainingPercent != nil {
			q.Windows = append(q.Windows, types.QuotaWindow{Name: l.name, Label: l.label, UsedPercent: usedFromRemaining(*l.w.RemainingPercent), ResetsAt: parseQuotaTime(l.w.ResetsAt)})
		}
	}
	return q, nil
}
