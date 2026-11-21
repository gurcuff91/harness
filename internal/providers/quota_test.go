package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// The bodies below are the REAL answers of each usage endpoint (2026-11-21),
// personal fields removed; opencode-go's is the shape of opencode's own client
// fixture (no live key).

const claudeUsageBody = `{"five_hour":{"utilization":21.0,"resets_at":"2026-11-21T18:19:59.609761+00:00","limit_dollars":null},"seven_day":{"utilization":31.0,"resets_at":"2026-11-26T16:59:59.609780+00:00"},"seven_day_opus":null,"extra_usage":{"is_enabled":false},"limits":[{"kind":"session","group":"session","percent":21,"severity":"normal","resets_at":"2026-11-21T18:19:59.609761+00:00","scope":null,"is_active":false},{"kind":"weekly_all","group":"weekly","percent":31,"severity":"normal","resets_at":"2026-11-26T16:59:59.609780+00:00","scope":null,"is_active":true},{"kind":"weekly_scoped","group":"weekly","percent":0,"severity":"normal","resets_at":"2026-11-26T17:00:00+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}],"spend":{"percent":0}}`

const codexUsageBody = `{"plan_type":"free","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0,"limit_window_seconds":2592000,"reset_after_seconds":2076999,"reset_at":1797346821},"secondary_window":null},"code_review_rate_limit":null,"additional_rate_limits":null,"credits":{"has_credits":false}}`

const minimaxUsageBody = `{"model_remains":[{"start_time":1795255200000,"end_time":1795273200000,"remains_time":3376615,"current_interval_total_count":0,"current_interval_usage_count":0,"model_name":"general","current_weekly_total_count":0,"current_weekly_usage_count":0,"weekly_start_time":1794787200000,"weekly_end_time":1795392000000,"weekly_remains_time":122176615,"current_interval_status":1,"current_interval_remaining_percent":92,"current_weekly_status":1,"current_weekly_remaining_percent":32},{"start_time":1795219200000,"end_time":1795305600000,"model_name":"video","weekly_start_time":1794787200000,"weekly_end_time":1795392000000,"current_interval_remaining_percent":100,"current_weekly_remaining_percent":100}],"base_resp":{"status_code":0,"status_msg":"success"}}`

const syntheticUsageBody = `{"subscription":{"limit":500,"requests":0,"renewsAt":"2026-11-21T19:03:43.549Z"},"search":{"hourly":{"limit":250,"requests":0,"renewsAt":"2026-11-21T15:03:43.550Z"}},"planWeeklyLimit":{"nextRegenAt":"2026-11-21T14:59:54.000Z","percentRemaining":99.99999476190476,"nextRegenPercent":2}}`

const openCodeGoUsageBody = `{"usage":{"rolling":{"status":"ok","percent":42,"resetsAt":"2026-10-24T17:00:00.000Z"},"weekly":{"status":"ok","percent":10.5,"resetsAt":"2026-10-27T00:00:00.000Z"},"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-11-01T00:00:00.000Z"}}}`

const ollamaBalanceBody = `{"included":{"balance_usd":0,"allowance_usd":0,"period":{"from":"2026-10-22T17:47:00Z","until":"2026-11-22T17:47:00Z"}},"purchased":{"balance_usd":0}}`

var quotaNow = time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func checkWindows(t *testing.T, q *types.Quota, want []types.QuotaWindow) {
	t.Helper()
	if len(q.Windows) != len(want) {
		t.Fatalf("got %d windows %+v, want %d", len(q.Windows), q.Windows, len(want))
	}
	for i, w := range want {
		g := q.Windows[i]
		if g.Name != w.Name || g.Label != w.Label || g.Scope != w.Scope || g.UsedPercent != w.UsedPercent || !g.ResetsAt.Equal(w.ResetsAt) {
			t.Errorf("window %d = %+v\nwant          %+v", i, g, w)
		}
		if !g.ResetsAt.IsZero() && g.ResetsAt.Location() != time.UTC {
			t.Errorf("window %d resets_at not UTC: %v", i, g.ResetsAt)
		}
	}
}

func TestParseClaudeQuota(t *testing.T) {
	q, err := parseClaudeQuota([]byte(claudeUsageBody), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	if q.Provider != "claude-oauth" || !q.FetchedAt.Equal(quotaNow) {
		t.Fatalf("provider/fetched_at = %q/%v", q.Provider, q.FetchedAt)
	}
	checkWindows(t, q, []types.QuotaWindow{
		{Name: "session", Label: "5-hour session", UsedPercent: 21, ResetsAt: mustTime("2026-11-21T18:19:59.609761Z")},
		{Name: "weekly", Label: "Weekly (all models)", UsedPercent: 31, ResetsAt: mustTime("2026-11-26T16:59:59.609780Z")},
		{Name: "weekly:Fable", Label: "Weekly (Fable)", Scope: "Fable", UsedPercent: 0, ResetsAt: mustTime("2026-11-26T17:00:00Z")},
	})
}

// Without limits[] (an older answer), five_hour/seven_day still give the two windows.
func TestParseClaudeQuotaFallsBackToSummary(t *testing.T) {
	q, err := parseClaudeQuota([]byte(`{"five_hour":{"utilization":5.5,"resets_at":"2026-11-21T18:00:00+02:00"},"seven_day":{"utilization":null}}`), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	checkWindows(t, q, []types.QuotaWindow{{Name: "session", Label: "5-hour session", UsedPercent: 5.5, ResetsAt: mustTime("2026-11-21T16:00:00Z")}})
}

func TestParseCodexQuota(t *testing.T) {
	q, err := parseCodexQuota([]byte(codexUsageBody), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "free" {
		t.Fatalf("plan = %q", q.Plan)
	}
	checkWindows(t, q, []types.QuotaWindow{{Name: "primary", Label: "30-day", UsedPercent: 0, ResetsAt: time.Unix(1797346821, 0).UTC()}})
}

// A paid plan: two windows plus an additional, named limit.
func TestParseCodexQuotaPaidPlan(t *testing.T) {
	body := `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":12.5,"limit_window_seconds":18000,"reset_at":1797300000},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_at":1797400000}},"additional_rate_limits":[{"limit_name":"GPT-5.3-Codex-Spark","metered_feature":"codex_spark","rate_limit":{"primary_window":{"used_percent":3,"limit_window_seconds":18000,"reset_at":1797300100},"secondary_window":null}}]}`
	q, err := parseCodexQuota([]byte(body), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	checkWindows(t, q, []types.QuotaWindow{
		{Name: "primary", Label: "5-hour", UsedPercent: 12.5, ResetsAt: time.Unix(1797300000, 0).UTC()},
		{Name: "secondary", Label: "7-day", UsedPercent: 40, ResetsAt: time.Unix(1797400000, 0).UTC()},
		{Name: "GPT-5.3-Codex-Spark:primary", Label: "5-hour (GPT-5.3-Codex-Spark)", Scope: "GPT-5.3-Codex-Spark", UsedPercent: 3, ResetsAt: time.Unix(1797300100, 0).UTC()},
	})
}

func TestParseMiniMaxQuota(t *testing.T) {
	q, err := parseMiniMaxQuota([]byte(minimaxUsageBody), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	checkWindows(t, q, []types.QuotaWindow{
		{Name: "general:interval", Label: "5-hour (general)", Scope: "general", UsedPercent: 8, ResetsAt: time.UnixMilli(1795273200000).UTC()},
		{Name: "general:weekly", Label: "Weekly (general)", Scope: "general", UsedPercent: 68, ResetsAt: time.UnixMilli(1795392000000).UTC()},
		{Name: "video:interval", Label: "1-day (video)", Scope: "video", UsedPercent: 0, ResetsAt: time.UnixMilli(1795305600000).UTC()},
		{Name: "video:weekly", Label: "Weekly (video)", Scope: "video", UsedPercent: 0, ResetsAt: time.UnixMilli(1795392000000).UTC()},
	})
}

// MiniMax answers 200 for errors too: base_resp decides.
func TestParseMiniMaxQuotaReadsBaseRespError(t *testing.T) {
	_, err := parseMiniMaxQuota([]byte(`{"model_remains":null,"base_resp":{"status_code":1004,"status_msg":"cookie is missing, log in again"}}`), quotaNow)
	if err == nil || !strings.Contains(err.Error(), "cookie is missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSyntheticQuota(t *testing.T) {
	q, err := parseSyntheticQuota([]byte(syntheticUsageBody), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	// 99.99999476… remaining is 0 used at two decimals, not 0.0000052…
	checkWindows(t, q, []types.QuotaWindow{
		{Name: "requests", Label: "Subscription requests", UsedPercent: 0, ResetsAt: mustTime("2026-11-21T19:03:43.549Z")},
		{Name: "weekly", Label: "Weekly (rolling)", UsedPercent: 0, ResetsAt: mustTime("2026-11-21T14:59:54Z")},
		{Name: "search:hourly", Label: "Hourly (search)", Scope: "search", UsedPercent: 0, ResetsAt: mustTime("2026-11-21T15:03:43.550Z")},
	})
	q, _ = parseSyntheticQuota([]byte(`{"subscription":{"limit":200,"requests":50,"renewsAt":""}}`), quotaNow)
	if q.Windows[0].UsedPercent != 25 || !q.Windows[0].ResetsAt.IsZero() {
		t.Fatalf("requests window = %+v", q.Windows[0])
	}
	q, _ = parseSyntheticQuota([]byte(`{}`), quotaNow)
	if q.Windows == nil || len(q.Windows) != 0 {
		t.Fatalf("credits-only account: windows = %#v, want an empty array", q.Windows)
	}
}

func TestParseOpenCodeGoQuota(t *testing.T) {
	q, err := parseOpenCodeGoQuota([]byte(openCodeGoUsageBody), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	checkWindows(t, q, []types.QuotaWindow{
		{Name: "rolling", Label: "Rolling", UsedPercent: 42, ResetsAt: mustTime("2026-10-24T17:00:00Z")},
		{Name: "weekly", Label: "Weekly", UsedPercent: 10.5, ResetsAt: mustTime("2026-10-27T00:00:00Z")},
		{Name: "monthly", Label: "Monthly", UsedPercent: 100, ResetsAt: mustTime("2026-11-01T00:00:00Z")},
	})
}

func TestParseOllamaQuota(t *testing.T) {
	q, err := parseOllamaQuota([]byte(ollamaBalanceBody), quotaNow)
	if err != nil {
		t.Fatal(err)
	}
	if q.Balance == nil || q.Balance.RemainingUSD != 0 || q.Balance.AllowanceUSD != 0 || !q.Balance.ResetsAt.Equal(mustTime("2026-11-22T17:47:00Z")) {
		t.Fatalf("balance = %+v", q.Balance)
	}
	if len(q.Windows) != 0 {
		t.Fatalf("a zero allowance has no window: %+v", q.Windows)
	}
	// A paid plan: $12 left of $20 included, plus $5 purchased.
	q, _ = parseOllamaQuota([]byte(`{"included":{"balance_usd":12,"allowance_usd":20,"period":{"until":"2026-12-01T00:00:00Z"}},"purchased":{"balance_usd":5}}`), quotaNow)
	if q.Balance.RemainingUSD != 17 || q.Balance.AllowanceUSD != 20 {
		t.Fatalf("balance = %+v", q.Balance)
	}
	checkWindows(t, q, []types.QuotaWindow{{Name: "allowance", Label: "Included allowance", UsedPercent: 40, ResetsAt: mustTime("2026-12-01T00:00:00Z")}})
	// A legacy plan reports what remains per window.
	q, _ = parseOllamaQuota([]byte(`{"included":{"session":{"remaining_percent":75,"resets_at":"2026-11-21T19:00:00Z"},"weekly":{"remaining_percent":90,"resets_at":"2026-11-24T00:00:00Z"}}}`), quotaNow)
	if q.Balance != nil {
		t.Fatalf("legacy plan has no balance: %+v", q.Balance)
	}
	checkWindows(t, q, []types.QuotaWindow{
		{Name: "session", Label: "Session", UsedPercent: 25, ResetsAt: mustTime("2026-11-21T19:00:00Z")},
		{Name: "weekly", Label: "Weekly", UsedPercent: 10, ResetsAt: mustTime("2026-11-24T00:00:00Z")},
	})
}

func TestQuotaNormalisationHelpers(t *testing.T) {
	if clampPercent(-3) != 0 || clampPercent(130) != 100 || clampPercent(42) != 42 || clampPercent(12.3456) != 12.35 || usedFromRemaining(99.99999476190476) != 0 {
		t.Fatal("clampPercent")
	}
	if usedFromRemaining(92) != 8 || usedFromRemaining(120) != 0 {
		t.Fatal("usedFromRemaining")
	}
	if !parseQuotaTime("bogus").IsZero() || !parseQuotaTime("").IsZero() {
		t.Fatal("parseQuotaTime must give zero for bad input")
	}
	for d, want := range map[time.Duration]string{5 * time.Hour: "5-hour", 7 * 24 * time.Hour: "7-day", 90 * time.Minute: "90-minute", 0: ""} {
		if got := periodLabel(d); got != want {
			t.Errorf("periodLabel(%v) = %q, want %q", d, got, want)
		}
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if parseRetryAfter("30", now) != 30*time.Second || parseRetryAfter("", now) != 0 || parseRetryAfter("-1", now) != 0 {
		t.Fatal("parseRetryAfter seconds")
	}
	if got := parseRetryAfter(now.Add(2*time.Minute).Format(http.TimeFormat), now); got != 2*time.Minute {
		t.Fatalf("parseRetryAfter date = %v", got)
	}
}

// ── End to end against a fake endpoint ───────────────────────────────────

// swapURL points a usage-URL variable at a test server for the test.
func swapURL(t *testing.T, v *string, to string) {
	t.Helper()
	prev := *v
	*v = to
	t.Cleanup(func() { *v = prev })
}

func TestMiniMaxQuotaSendsItsKeyAndParses(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(minimaxUsageBody))
	}))
	t.Cleanup(srv.Close)
	swapURL(t, &minimaxQuotaURL, srv.URL)
	m := &MiniMax{apiKey: "sk-test", client: srv.Client()}
	q, err := m.Quota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" || len(q.Windows) != 4 {
		t.Fatalf("auth=%q windows=%d", auth, len(q.Windows))
	}
}

func TestQuotaHTTPErrorCarriesStatusRetryAfterAndReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)
	swapURL(t, &syntheticQuotaURL, srv.URL)
	s := NewSynthetic()
	s.apiKey = "k"
	_, err := s.Quota(context.Background())
	var he *QuotaHTTPError
	if !errors.As(err, &he) || he.Status != 429 || he.RetryAfter != 42*time.Second || he.Reason != "rate_limit_error" {
		t.Fatalf("err = %#v", err)
	}
}

// opencode's own client reads a 403 as "this key has no Go subscription".
func TestOpenCodeGoQuota403IsNoQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	t.Cleanup(srv.Close)
	swapURL(t, &openCodeGoUsage, srv.URL)
	o := NewOpenCodeGo()
	o.apiKey = "k"
	if _, err := o.Quota(context.Background()); !errors.Is(err, ErrNoQuota) {
		t.Fatalf("err = %v, want ErrNoQuota", err)
	}
}

// The plan comes from /api/me; a failing /api/me leaves it empty and the quota stands.
func TestOllamaQuotaReadsPlanBestEffort(t *testing.T) {
	failMe := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/balance":
			w.Write([]byte(ollamaBalanceBody))
		case "/me":
			if r.Method != http.MethodPost || failMe {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"ID":"x","Plan":"free"}`))
		}
	}))
	t.Cleanup(srv.Close)
	swapURL(t, &ollamaBalanceURL, srv.URL+"/balance")
	swapURL(t, &ollamaMeURL, srv.URL+"/me")
	o := &OllamaCloud{apiKey: "k", client: srv.Client(), baseURL: srv.URL}
	q, err := o.Quota(context.Background())
	if err != nil || q.Plan != "free" || q.Balance == nil {
		t.Fatalf("q=%+v err=%v", q, err)
	}
	failMe = true
	q, err = o.Quota(context.Background())
	if err != nil || q.Plan != "" || q.Balance == nil {
		t.Fatalf("q=%+v err=%v", q, err)
	}
}

func TestQuotaWithoutCredentialsIsNoUpstream(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	s := NewSynthetic()
	s.apiKey = ""
	t.Setenv("SYNTHETIC_API_KEY", "")
	if _, err := s.Quota(context.Background()); !errors.Is(err, ErrNoUpstream) {
		t.Fatalf("err = %v, want ErrNoUpstream", err)
	}
}
