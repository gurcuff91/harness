package tui

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// /info shows the session id, the system and the terminal (what the
// SessionInfo tool reports), and, when there are any, the channels.
func TestInfoPanelShowsIdSystemAndTerminal(t *testing.T) {
	f := newFallbackFixtureWith(t, agent.AgentOptions{Terminal: "Workspace (t9)"})
	tu := f.tui(f.sessionID)
	if err := tu.autoConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	tu.showInfo()
	got := stripANSI(screenText(tu))
	for _, want := range []string{
		"id        " + f.sessionID,
		"system    " + runtime.GOOS + "/" + runtime.GOARCH,
		"terminal  Workspace (t9)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel lacks %q:\n%s", want, got)
		}
	}
}

// An agent with no terminal of its own (serve) shows no terminal line.
func TestInfoPanelOmitsAnUnknownTerminal(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui(f.sessionID)
	if err := tu.autoConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	tu.showInfo()
	if got := stripANSI(screenText(tu)); strings.Contains(got, "terminal  ") {
		t.Errorf("a terminal line without a terminal:\n%s", got)
	}
}

// withQuota wraps a provider with a fixed quota, as a QuotaReporter.
type withQuota struct {
	providers.Provider
	q *types.Quota
}

func (w withQuota) Quota(context.Context) (*types.Quota, error) { return w.q, nil }

// /info shows the quota of the SESSION's provider; one without a quota shows no section.
func TestInfoPanelShowsTheSessionProvidersQuota(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui(f.sessionID)
	if err := tu.autoConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	tu.showInfo()
	if got := stripANSI(screenText(tu)); strings.Contains(got, "quota") {
		t.Fatalf("a provider without quota shows a quota section:\n%s", got)
	}

	q := &types.Quota{Provider: f.first, Plan: "pro", FetchedAt: time.Now().UTC(), Windows: []types.QuotaWindow{
		{Name: "weekly", Label: "Weekly (all models)", UsedPercent: 42, ResetsAt: time.Now().Add(3 * time.Hour)},
	}}
	var list []providers.Provider
	for _, p := range providers.All() {
		if p.Name() == f.first {
			p = withQuota{p, q}
		}
		list = append(list, p)
	}
	t.Cleanup(providers.SetForTest(list))
	tu.showInfo()
	got := stripANSI(screenText(tu))
	for _, want := range []string{"quota     " + f.first + " · pro", "weekly    ████░░░░░░  42%   resets in 2h 59m"} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel lacks %q:\n%s", want, got)
		}
	}
}
