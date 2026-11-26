package admin_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

func TestProvidersListAndModelSemantics(t *testing.T) {
	good := newFake("good", "alpha", "org/beta:7b")
	off := newFake("off", "x")
	off.on.Store(false)
	broken := newFake("broken", "x")
	broken.fetchErr = errors.New("token expired")
	ad := newAdmin(t)
	useProviders(t, good, off, broken)
	ctx := context.Background()

	list, err := ad.Providers.List(ctx)
	if err != nil || len(list) != 3 || list[0].Name != "good" || list[0].ModelCount != 2 || list[0].Activation != "auto" || list[0].CredentialType != "api_key" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list[2].Error != "token expired" || list[2].ModelCount != 0 {
		t.Fatalf("broken provider must report its error: %+v", list[2])
	}

	all, err := ad.Providers.Models(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("unfiltered skips failing providers: %v %v", all, err)
	}
	if one, err := ad.Providers.Models(ctx, "good"); err != nil || len(one) != 2 || one[0].Model != "good/alpha" {
		t.Fatalf("filtered: %v %v", one, err)
	}
	if _, err := ad.Providers.Models(ctx, "nope"); !errors.Is(err, admin.ErrNotFound) || err.Error() != `provider "nope" not found` {
		t.Fatalf("404: %v", err)
	}
	if _, err := ad.Providers.Models(ctx, "off"); !errors.Is(err, admin.ErrNotConnected) || err.Error() != `provider "off" is not connected` {
		t.Fatalf("409: %v", err)
	}
	_, err = ad.Providers.Models(ctx, "broken")
	if !errors.Is(err, admin.ErrUpstream) || !strings.Contains(err.Error(), "could not list models: token expired") {
		t.Fatalf("502: %v", err)
	}

	if m, err := ad.Providers.Model(ctx, "good", "org/beta:7b"); err != nil || m.Model != "good/org/beta:7b" {
		t.Fatalf("model: %v %v", m, err)
	}
	if _, err := ad.Providers.Model(ctx, "good", "zzz"); !errors.Is(err, admin.ErrNotFound) || err.Error() != `model "zzz" not found in provider "good"` {
		t.Fatalf("unknown model: %v", err)
	}
	if _, err := ad.Providers.Model(ctx, "nope", "a"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatal("unknown provider")
	}
	if _, err := ad.Providers.Model(ctx, "off", "a"); !errors.Is(err, admin.ErrNotConnected) {
		t.Fatal("not connected")
	}
	if _, err := ad.Providers.Model(ctx, "broken", "a"); !errors.Is(err, admin.ErrUpstream) {
		t.Fatal("upstream")
	}
}

func TestConnectDisconnect(t *testing.T) {
	p := newFake("p", "a", "b")
	ad := newAdmin(t)
	useProviders(t, p)
	ctx := context.Background()

	n, err := ad.Providers.Connect(ctx, "p", admin.ConnectCredentials{APIKey: "k", AccountID: "acc"})
	if err != nil || n != 2 {
		t.Fatalf("connect: %d %v", n, err)
	}
	if p.gotCreds.APIKey != "k" || p.gotCreds.AccountID != "acc" || p.gotCreds.Type != types.CredTypeAPIKey {
		t.Fatalf("creds: %+v", p.gotCreds)
	}
	if _, err := ad.Providers.Connect(ctx, "zz", admin.ConnectCredentials{}); !errors.Is(err, admin.ErrNotFound) || err.Error() != "provider not found: zz" {
		t.Fatalf("unknown: %v", err)
	}
	p.connect = func(types.Credentials) error { return errors.New("bad key") }
	if _, err := ad.Providers.Connect(ctx, "p", admin.ConnectCredentials{}); !errors.Is(err, admin.ErrInvalid) || err.Error() != "bad key" {
		t.Fatalf("refused: %v", err)
	}
	if err := ad.Providers.Disconnect("zz"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatal("disconnect unknown")
	}
	if err := ad.Providers.Disconnect("p"); err != nil {
		t.Fatal(err)
	}
	if ct, err := ad.Providers.CredentialType("p"); err != nil || ct != types.CredTypeAPIKey {
		t.Fatal("credential type")
	}
}

func okQuota() (*types.Quota, error) {
	return &types.Quota{Provider: "q", FetchedAt: time.Now().UTC(), Windows: []types.QuotaWindow{{Name: "weekly", UsedPercent: 10}}}, nil
}

func TestQuotaErrorsAndConnectForgetsTheCache(t *testing.T) {
	q := quotaFake{newFake("q", "a")}
	q.quota = okQuota
	plain := newFake("plain")
	ad := newAdmin(t)
	useProviders(t, q, plain)
	t.Cleanup(providers.SetQuotaRulesForTest(time.Hour, 0, time.Minute))
	ctx := context.Background()

	if _, _, err := ad.Providers.Quota(ctx, "nope", false); !errors.Is(err, admin.ErrNotFound) {
		t.Fatal("unknown")
	}
	if _, _, err := ad.Providers.Quota(ctx, "plain", false); !errors.Is(err, admin.ErrNotFound) || !strings.Contains(err.Error(), "does not report a quota") {
		t.Fatalf("no reporter: %v", err)
	}

	// Cache: two reads, one call.
	if _, _, err := ad.Providers.Quota(ctx, "q", false); err != nil {
		t.Fatal(err)
	}
	ad.Providers.Quota(ctx, "q", false)
	if q.quotaCalls.Load() != 1 {
		t.Fatalf("cache not used: %d calls", q.quotaCalls.Load())
	}
	// Connect forgets the previous account's reading.
	if _, err := ad.Providers.Connect(ctx, "q", admin.ConnectCredentials{APIKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	ad.Providers.Quota(ctx, "q", false)
	if q.quotaCalls.Load() != 2 {
		t.Fatalf("connect must drop the cached quota: %d calls", q.quotaCalls.Load())
	}
	// So does disconnect.
	if err := ad.Providers.Disconnect("q"); err != nil {
		t.Fatal(err)
	}
	ad.Providers.Quota(ctx, "q", false)
	if q.quotaCalls.Load() != 3 {
		t.Fatalf("disconnect must drop the cached quota: %d calls", q.quotaCalls.Load())
	}

	// Error mapping (each on a fresh cache entry).
	providers.ForgetQuota("q")
	q.quota = func() (*types.Quota, error) { return nil, providers.ErrNoUpstream }
	if _, _, err := ad.Providers.Quota(ctx, "q", true); !errors.Is(err, admin.ErrNotConnected) || !strings.Contains(err.Error(), `provider "q" is not connected`) {
		t.Fatalf("409: %v", err)
	}
	q.quota = func() (*types.Quota, error) { return nil, providers.ErrNoQuota }
	if _, _, err := ad.Providers.Quota(ctx, "q", true); !errors.Is(err, admin.ErrNotFound) || !strings.Contains(err.Error(), "has no quota for this account") {
		t.Fatalf("404: %v", err)
	}
	providers.ForgetQuota("q")
	q.quota = func() (*types.Quota, error) {
		return nil, &providers.QuotaHTTPError{Provider: "q", Status: 429, RetryAfter: 30 * time.Second}
	}
	_, retry, err := ad.Providers.Quota(ctx, "q", true)
	if !errors.Is(err, admin.ErrUpstream) || retry <= 0 || retry > 30*time.Second {
		t.Fatalf("502 with Retry-After: %v %v", err, retry)
	}
}

func TestCustomProviders(t *testing.T) {
	ad := newAdmin(t)
	useProviders(t)
	ctx := context.Background()
	_ = ctx

	if err := ad.Providers.PutCustom("mine", types.CustomProvider{Type: "openai", URL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if got := ad.Providers.ListCustom(); len(got) != 1 {
		t.Fatalf("list: %v", got)
	}
	err := ad.Providers.PutCustom("bad", types.CustomProvider{Type: "nonsense", URL: "x"})
	if !errors.Is(err, admin.ErrUnprocessable) {
		t.Fatalf("invalid must be ErrUnprocessable: %v", err)
	}
	if err := ad.Providers.ReloadCustom(); err != nil {
		t.Fatal(err)
	}
	if find := findProvider(ad, "mine"); !find {
		t.Fatal("reload must register the declared provider")
	}
	if err := ad.Providers.DeleteCustom("ghost"); !errors.Is(err, admin.ErrNotFound) || err.Error() != "custom provider not found: ghost" {
		t.Fatalf("delete absent: %v", err)
	}
	if err := ad.Providers.DeleteCustom("mine"); err != nil {
		t.Fatal(err)
	}
	ad.Providers.ReloadCustom()
	if findProvider(ad, "mine") {
		t.Fatal("reload must drop the deleted provider")
	}
}

func findProvider(ad *admin.Admin, name string) bool {
	l, _ := ad.Providers.List(context.Background())
	for _, p := range l {
		if p.Name == name {
			return true
		}
	}
	return false
}

func TestProgrammaticRegistration(t *testing.T) {
	ad := newAdmin(t)
	useProviders(t)
	ctx := context.Background()

	err := ad.Providers.RegisterOpenAI("prog", "http://127.0.0.1:1",
		admin.ProviderWithDisplay("Prog"),
		admin.ProviderWithHeaders(map[string]string{"X-Api-Key": "k"}),
		admin.ProviderWithFetchModels(func() ([]types.ModelMeta, error) { return []types.ModelMeta{{ID: "m1"}}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Providers.RegisterOpenAI("prog", "http://127.0.0.1:1"); err == nil {
		t.Fatal("duplicate must fail")
	}
	if err := ad.Providers.RegisterAnthropic("anthropic", "http://x"); err == nil {
		t.Fatal("built-in name must fail")
	}
	if err := ad.Providers.RegisterAnthropic("prog-a", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	ms, err := ad.Providers.Models(ctx, "prog")
	if err != nil || len(ms) != 1 || ms[0].Model != "prog/m1" {
		t.Fatalf("models: %v %v", ms, err)
	}
	if err := ad.Providers.Unregister("prog"); err != nil {
		t.Fatal(err)
	}
	if err := ad.Providers.Unregister("prog"); !errors.Is(err, admin.ErrProviderNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	ad.Providers.PutCustom("decl", types.CustomProvider{Type: "openai", URL: "http://127.0.0.1:1"})
	ad.Providers.ReloadCustom()
	if err := ad.Providers.Unregister("decl"); !errors.Is(err, admin.ErrProviderNotRemovable) {
		t.Fatalf("settings-declared: %v", err)
	}
	ad.Providers.Unregister("prog-a")
}

func TestOAuthErrors(t *testing.T) {
	ad := newAdmin(t)
	if _, _, err := ad.Providers.OAuthStart("openai"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("no flow: %v", err)
	}
	if _, err := ad.Providers.OAuthExchange("openai", "c", "v"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("no flow: %v", err)
	}
	if _, err := ad.Providers.OAuthExchange("claude-oauth", "c", ""); !errors.Is(err, admin.ErrInvalid) || err.Error() != "verifier_code is required for exchange" {
		t.Fatalf("verifier: %v", err)
	}
	u, v, err := ad.Providers.OAuthStart("claude-oauth")
	if err != nil || u == "" || v == "" {
		t.Fatalf("claude start: %q %q %v", u, v, err)
	}
}
