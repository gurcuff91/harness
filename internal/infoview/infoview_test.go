package infoview

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

var now = time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)

func TestWindowLabel(t *testing.T) {
	for _, c := range []struct {
		w            types.QuotaWindow
		label, scope string
	}{
		{types.QuotaWindow{Name: "session", Label: "5-hour session"}, "session", ""},
		{types.QuotaWindow{Name: "weekly", Label: "Weekly (all models)"}, "weekly", ""},
		{types.QuotaWindow{Name: "weekly:Fable", Label: "Weekly (Fable)", Scope: "Fable"}, "Fable", ""},
		{types.QuotaWindow{Name: "weekly:Very Long Model", Label: "Weekly (Very Long Model)", Scope: "Very Long Model"}, "weekly", "Very Long Model"},
		{types.QuotaWindow{Name: "primary", Label: "30-day"}, "30-day", ""},
		{types.QuotaWindow{Name: "general:weekly", Label: "Weekly (general)", Scope: "general"}, "weekly", "general"},
		{types.QuotaWindow{Name: "requests", Label: "Subscription requests"}, "requests", ""},
		{types.QuotaWindow{Name: "search:hourly", Label: "Hourly (search)", Scope: "search"}, "hourly", "search"},
		{types.QuotaWindow{Name: "GPT-5.3-Codex-Spark:primary", Label: "5-hour (GPT-5.3-Codex-Spark)", Scope: "GPT-5.3-Codex-Spark"}, "5-hour", "GPT-5.3-Codex-Spark"},
		{types.QuotaWindow{Name: "x:averyveryverylongname", Label: "Extraordinarily long"}, "averyvery", ""},
	} {
		l, s := WindowLabel(c.w)
		if l != c.label || s != c.scope {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.w.Name, l, s, c.label, c.scope)
		}
	}
}

func TestBarLevelPercent(t *testing.T) {
	for used, want := range map[float64]string{0: "░░░░░░░░░░", 0.4: "█░░░░░░░░░", 23: "██░░░░░░░░", 100: "██████████", 150: "██████████", -5: "░░░░░░░░░░"} {
		if got := Bar(used); got != want {
			t.Errorf("Bar(%v) = %q, want %q", used, got, want)
		}
	}
	for used, want := range map[float64]int{0: LevelNormal, 69.9: LevelNormal, 70: LevelHigh, 90: LevelHigh, 90.1: LevelCritical} {
		if Level(used) != want {
			t.Errorf("Level(%v) = %d, want %d", used, Level(used), want)
		}
	}
	if Percent(5) != "  5%" || Percent(100) != "100%" {
		t.Errorf("Percent: %q %q", Percent(5), Percent(100))
	}
}

func TestHeaderDetailBalanceAndTimes(t *testing.T) {
	q := &types.Quota{Plan: "free", Stale: true, FetchedAt: now.Add(-3 * time.Minute)}
	if Header("codex-oauth", q) != "codex-oauth · free" || Header("x", &types.Quota{}) != "x" {
		t.Error("Header")
	}
	if StaleNote(q, now) != " · stale (3m ago)" || StaleNote(&types.Quota{}, now) != "" {
		t.Errorf("StaleNote = %q", StaleNote(q, now))
	}
	if d := WindowDetail("general", now.Add(45*time.Minute), now); d != "general · resets in 45m" {
		t.Errorf("WindowDetail = %q", d)
	}
	if WindowDetail("", time.Time{}, now) != "" || WindowDetail("video", time.Time{}, now) != "video" {
		t.Error("WindowDetail parts")
	}
	if Balance(&types.QuotaBalance{RemainingUSD: 12.5, AllowanceUSD: 20}) != "$12.50 of $20.00" {
		t.Error("Balance")
	}
	for d, want := range map[time.Duration]string{-time.Second: "now", 30 * time.Second: "30s", 45 * time.Minute: "45m", 4*time.Hour + 5*time.Minute: "4h 05m", 50 * time.Hour: "2d 02h"} {
		if got := Until(now.Add(d), now); got != want {
			t.Errorf("Until(%v) = %q, want %q", d, got, want)
		}
	}
	if ProviderOf("synthetic/hf:zai-org/GLM-4.7") != "synthetic" || ProviderOf("") != "" {
		t.Error("ProviderOf")
	}
}

func TestNoQuota(t *testing.T) {
	if !NoQuota(&client.Error{Status: 404}) || !NoQuota(fmt.Errorf("wrapped: %w", &client.Error{Status: 404})) {
		t.Error("a 404 is no quota")
	}
	if NoQuota(&client.Error{Status: 502}) || NoQuota(errors.New("x")) || NoQuota(nil) {
		t.Error("anything else is a failure")
	}
}

func TestChannelGroups(t *testing.T) {
	g := ChannelGroups([]client.ChannelInfo{
		{Type: "slack", ID: "C1", Name: "#a"}, {Type: "telegram", ID: "T1", Name: "@bot"}, {Type: "slack", ID: "D9"},
	})
	if len(g) != 2 || g[0].Type != "slack" || strings.Join(g[0].Names, ",") != "#a,D9" || g[1].Type != "telegram" {
		t.Errorf("groups = %+v", g)
	}
}
