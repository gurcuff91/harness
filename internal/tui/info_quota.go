package tui

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/types"
)

// quotaBarCells is the width of a usage bar in /info.
const quotaBarCells = 10

// providerOf returns the provider part of a "provider/model" string.
func providerOf(model string) string {
	p, _, _ := strings.Cut(model, "/")
	return p
}

// quotaSection renders the quota block of /info for the session's provider.
// It returns "" when the provider reports no quota (404: nothing to say);
// any other failure is one dimmed line, so /info never breaks because of it.
func quotaSection(provider string, q *types.Quota, err error, now time.Time) string {
	label := func(l string) string { return "  " + ansi.Muted(fmt.Sprintf("%-10s", l)) }
	if err != nil {
		var ce *client.Error
		if errors.As(err, &ce) && ce.Status == 404 {
			return ""
		}
		return label("quota") + ansi.Dimmed("unavailable ("+err.Error()+")") + "\n"
	}
	if q == nil {
		return ""
	}
	var b strings.Builder
	head := provider
	if q.Plan != "" {
		head += " · " + q.Plan
	}
	if q.Stale {
		head += ansi.Dimmed(" · stale (" + ago(now.Sub(q.FetchedAt)) + " ago)")
	}
	b.WriteString(label("quota") + head + "\n")
	for _, w := range q.Windows {
		short, scope := shortWindowLabel(w)
		detail := "resets in " + until(w.ResetsAt, now)
		if w.ResetsAt.IsZero() {
			detail = ""
		}
		if scope != "" {
			if detail != "" {
				detail = scope + " · " + detail
			} else {
				detail = scope
			}
		}
		line := label(short) + quotaBar(w.UsedPercent) + fmt.Sprintf(" %4s", fmt.Sprintf("%.0f%%", w.UsedPercent))
		if detail != "" {
			line += "   " + ansi.Dimmed(detail)
		}
		b.WriteString(line + "\n")
	}
	if bal := q.Balance; bal != nil {
		line := label("balance") + fmt.Sprintf("$%.2f of $%.2f", bal.RemainingUSD, bal.AllowanceUSD)
		if !bal.ResetsAt.IsZero() {
			line += "   " + ansi.Dimmed("resets in "+until(bal.ResetsAt, now))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// shortWindowLabel fits a window in the 10-character label column:
//   - claude's two main windows: "session", "weekly";
//   - a per-model weekly window ("weekly:<model>"): the model itself, which
//     says more than the period (the reset time implies it);
//   - otherwise the label's first word when it fits ("5-hour", "30-day",
//     "rolling"), else the last part of the stable name ("requests");
//
// the scope, when not used as the label, goes to the detail column.
func shortWindowLabel(w types.QuotaWindow) (label, scope string) {
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

// quotaBar draws a 10-cell bar, colored by how close it is to the limit:
// normal below 70 %, warn up to 90 %, error above.
func quotaBar(used float64) string {
	filled := int(math.Round(used / 100 * quotaBarCells))
	if used > 0 && filled == 0 {
		filled = 1 // any use is visible
	}
	filled = max(0, min(quotaBarCells, filled))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", quotaBarCells-filled)
	switch {
	case used > 90:
		return ansi.Err(bar)
	case used >= 70:
		return ansi.Warn(bar)
	}
	return bar
}

// until formats the time left before t: "45m", "4h 05m", "5d 02h"; "now"
// when it is past.
func until(t, now time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "now"
	}
	return ago(d)
}

// ago formats a duration compactly: "30s", "45m", "4h 05m", "5d 02h".
func ago(d time.Duration) string {
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
