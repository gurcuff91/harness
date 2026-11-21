package acp

import (
	"strings"
	"time"

	"github.com/gurcuff91/harness/internal/infoview"
	"github.com/gurcuff91/harness/types"
)

// quotaRows renders the quota rows of /info with the panel's own row func,
// using the layout shared with the TUI (internal/infoview). Plain text: the
// panel is a Markdown code fence, where colors do not exist, so a window near
// its limit is marked with words instead (" ⚠ high" from 70 %, " ⚠ almost
// out" above 90 %). "" when the provider has no quota (404, or nothing
// fetched); any other failure is one "unavailable (...)" row.
func quotaRows(provider string, q *types.Quota, err error, now time.Time, row func(label, value string) string) string {
	if err != nil {
		if infoview.NoQuota(err) {
			return ""
		}
		return row("quota", "unavailable ("+err.Error()+")")
	}
	if q == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(row("quota", infoview.Header(provider, q)+infoview.StaleNote(q, now)))
	for _, w := range q.Windows {
		label, scope := infoview.WindowLabel(w)
		v := infoview.Bar(w.UsedPercent) + " " + infoview.Percent(w.UsedPercent)
		if d := infoview.WindowDetail(scope, w.ResetsAt, now); d != "" {
			v += "   " + d
		}
		switch infoview.Level(w.UsedPercent) {
		case infoview.LevelCritical:
			v += "  ⚠ almost out"
		case infoview.LevelHigh:
			v += "  ⚠ high"
		}
		b.WriteString(row(label, v))
	}
	if bal := q.Balance; bal != nil {
		v := infoview.Balance(bal)
		if !bal.ResetsAt.IsZero() {
			v += "   resets in " + infoview.Until(bal.ResetsAt, now)
		}
		b.WriteString(row("balance", v))
	}
	return b.String()
}
