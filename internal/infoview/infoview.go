// Package infoview holds the pieces of the /info panel shared by the
// frontends that show one — the TUI (internal/tui) and ACP (acp):
// the quota layout (window labels, usage bar, reset times) and the grouping of
// a session's channels. Pure formatting, no colors: each frontend styles the
// result its own way (the TUI colors the bar, ACP marks a high window with a
// word, since its panel is a Markdown code fence).
package infoview

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// BarCells is the width of a usage bar.
const BarCells = 10

// Usage levels of a window, for styling.
const (
	LevelNormal   = iota // below 70 %
	LevelHigh            // 70–90 %
	LevelCritical        // above 90 %
)

// Level classifies a usage percentage.
func Level(used float64) int {
	switch {
	case used > 90:
		return LevelCritical
	case used >= 70:
		return LevelHigh
	}
	return LevelNormal
}

// ProviderOf returns the provider part of a "provider/model" string.
func ProviderOf(model string) string {
	p, _, _ := strings.Cut(model, "/")
	return p
}

// Bar draws a BarCells-wide usage bar; any use fills at least one cell.
func Bar(used float64) string {
	filled := int(math.Round(used / 100 * BarCells))
	if used > 0 && filled == 0 {
		filled = 1
	}
	filled = max(0, min(BarCells, filled))
	return strings.Repeat("█", filled) + strings.Repeat("░", BarCells-filled)
}

// Percent formats a usage percentage right-aligned in 4 columns (" 23%").
func Percent(used float64) string { return fmt.Sprintf("%4s", fmt.Sprintf("%.0f%%", used)) }

// WindowLabel fits a window in a 10-column label:
//   - claude's two main windows: "session", "weekly";
//   - a per-model weekly window ("weekly:<model>"): the model itself, which
//     says more than the period (the reset time implies it);
//   - otherwise the label's first word when it fits ("5-hour", "30-day",
//     "rolling"), else the last part of the stable name ("requests").
//
// The scope, when not used as the label, is returned for the detail column.
func WindowLabel(w types.QuotaWindow) (label, scope string) {
	if w.Name == "session" || w.Name == "weekly" {
		return w.Name, w.Scope
	}
	if m, ok := strings.CutPrefix(w.Name, "weekly:"); ok && m == w.Scope && len([]rune(m)) <= 9 {
		return m, ""
	}
	first, _, _ := strings.Cut(w.Label, " ")
	first = strings.ToLower(first)
	if first == "" || len([]rune(first)) > 9 {
		first = w.Name
		if i := strings.LastIndex(first, ":"); i >= 0 {
			first = first[i+1:]
		}
		if r := []rune(first); len(r) > 9 {
			first = string(r[:9])
		}
	}
	return first, w.Scope
}

// WindowDetail is the detail column of a window: "<scope> · resets in 4h 05m",
// either part omitted when absent.
func WindowDetail(scope string, resetsAt, now time.Time) string {
	var parts []string
	if scope != "" {
		parts = append(parts, scope)
	}
	if !resetsAt.IsZero() {
		parts = append(parts, "resets in "+Until(resetsAt, now))
	}
	return strings.Join(parts, " · ")
}

// Header is the quota's first row value: "<provider>[ · <plan>]".
func Header(provider string, q *types.Quota) string {
	if q.Plan != "" {
		return provider + " · " + q.Plan
	}
	return provider
}

// StaleNote is " · stale (3m ago)" for a stale reading, "" otherwise.
func StaleNote(q *types.Quota, now time.Time) string {
	if !q.Stale {
		return ""
	}
	return " · stale (" + Ago(now.Sub(q.FetchedAt)) + " ago)"
}

// Balance is a balance row value: "$12.50 of $20.00" (reset separate).
func Balance(b *types.QuotaBalance) string {
	return fmt.Sprintf("$%.2f of $%.2f", b.RemainingUSD, b.AllowanceUSD)
}

// NoQuota reports whether a quota error means "this provider has none" (an
// API 404, or providers.ErrNoQuota / ErrProviderNotFound in process):
// the panel then shows no section at all.
func NoQuota(err error) bool {
	var ce *client.Error
	if errors.As(err, &ce) {
		return ce.Status == 404
	}
	// In-process readers (agent.SessionView) get the providers' own errors.
	return errors.Is(err, providers.ErrNoQuota) || errors.Is(err, providers.ErrProviderNotFound)
}

// Until formats the time left before t: "45m", "4h 05m", "5d 02h"; "now"
// when it is past.
func Until(t, now time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "now"
	}
	return Ago(d)
}

// Ago formats a duration compactly: "30s", "45m", "4h 05m", "5d 02h".
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dd %02dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
}

// ChannelGroup is the conversations of one channel type, in binding order.
type ChannelGroup struct {
	Type  string
	Names []string
}

// ChannelGroups groups a session's channels by type, keeping the binding
// order of both the types and the names (a channel with no name shows its id).
func ChannelGroups(chs []client.ChannelInfo) []ChannelGroup {
	var out []ChannelGroup
	at := map[string]int{}
	for _, c := range chs {
		n := c.Name
		if n == "" {
			n = c.ID
		}
		i, ok := at[c.Type]
		if !ok {
			i = len(out)
			at[c.Type] = i
			out = append(out, ChannelGroup{Type: c.Type})
		}
		out[i].Names = append(out[i].Names, n)
	}
	return out
}
