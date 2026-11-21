package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

var qNow = time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)

func TestQuotaSectionClaude(t *testing.T) {
	q := &types.Quota{Provider: "claude-oauth", FetchedAt: qNow, Windows: []types.QuotaWindow{
		{Name: "session", Label: "5-hour session", UsedPercent: 23, ResetsAt: qNow.Add(4*time.Hour + 5*time.Minute)},
		{Name: "weekly", Label: "Weekly (all models)", UsedPercent: 31, ResetsAt: qNow.Add(5*24*time.Hour + 2*time.Hour)},
		{Name: "weekly:Fable", Label: "Weekly (Fable)", Scope: "Fable", UsedPercent: 0, ResetsAt: qNow.Add(5*24*time.Hour + 2*time.Hour)},
	}}
	got := stripANSI(quotaSection("claude-oauth", q, nil, qNow))
	want := "" +
		"  quota     claude-oauth\n" +
		"  session   ██░░░░░░░░  23%   resets in 4h 05m\n" +
		"  weekly    ███░░░░░░░  31%   resets in 5d 02h\n" +
		"  Fable     ░░░░░░░░░░   0%   resets in 5d 02h\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestQuotaSectionPlanScopeStaleAndBalance(t *testing.T) {
	q := &types.Quota{Provider: "minimax", Plan: "free", FetchedAt: qNow.Add(-3 * time.Minute), Stale: true, Windows: []types.QuotaWindow{
		{Name: "general:interval", Label: "5-hour (general)", Scope: "general", UsedPercent: 8, ResetsAt: qNow.Add(45 * time.Minute)},
		{Name: "requests", Label: "Subscription requests", UsedPercent: 95},
	}, Balance: &types.QuotaBalance{RemainingUSD: 12.5, AllowanceUSD: 20, ResetsAt: qNow.Add(27 * time.Hour)}}
	got := stripANSI(quotaSection("minimax", q, nil, qNow))
	for _, want := range []string{
		"  quota     minimax · free · stale (3m ago)\n",
		"  5-hour    █░░░░░░░░░   8%   general · resets in 45m\n",
		"  requests  ██████████  95%\n",
		"  balance   $12.50 of $20.00   resets in 1d 03h\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q in:\n%s", want, got)
		}
	}
}

func TestQuotaSectionErrors(t *testing.T) {
	if s := quotaSection("anthropic", nil, &client.Error{Message: "no quota", Status: 404}, qNow); s != "" {
		t.Errorf("a provider without quota shows nothing, got %q", s)
	}
	got := stripANSI(quotaSection("opencode-go", nil, &client.Error{Message: "opencode-go usage endpoint: HTTP 401: api_key_expired", Status: 502}, qNow))
	if got != "  quota     unavailable (opencode-go usage endpoint: HTTP 401: api_key_expired)\n" {
		t.Errorf("got %q", got)
	}
	if got := stripANSI(quotaSection("x", nil, errors.New("dial tcp: refused"), qNow)); !strings.Contains(got, "unavailable (dial tcp: refused)") {
		t.Errorf("got %q", got)
	}
}

func TestQuotaBarColorsAndRounding(t *testing.T) {
	if b := quotaBar(0.4); stripANSI(b) != "█░░░░░░░░░" {
		t.Errorf("any use is visible: %q", stripANSI(b))
	}
	if b := quotaBar(0); stripANSI(b) != "░░░░░░░░░░" || strings.Contains(b, "\x1b") {
		t.Errorf("zero: %q", b)
	}
	if b := quotaBar(69); strings.Contains(b, "\x1b") {
		t.Error("below 70 % is not colored")
	}
	if quotaBar(70) == quotaBar(91) || !strings.Contains(quotaBar(70), "\x1b") || !strings.Contains(quotaBar(91), "\x1b") {
		t.Error("warn from 70 %, error above 90 %, different colors")
	}
	color := func(s string) string { return s[:strings.Index(s, "█")] }
	if color(quotaBar(90)) != color(quotaBar(70)) || color(quotaBar(91)) == color(quotaBar(90)) {
		t.Error("90 % is still warn")
	}
	if stripANSI(quotaBar(150)) != "██████████" {
		t.Error("clamped")
	}
}
