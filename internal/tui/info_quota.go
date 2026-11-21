package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gurcuff91/harness/internal/infoview"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/types"
)

// quotaSection renders the quota block of /info for the session's provider,
// with the layout shared with ACP (internal/infoview) and the TUI's styling:
// the bar colored by how close it is to the limit (warn from 70 %, error
// above 90 %), details dimmed. "" when the provider reports no quota (404:
// nothing to say); any other failure is one dimmed line, so /info never
// breaks because of it.
func quotaSection(provider string, q *types.Quota, err error, now time.Time) string {
	label := func(l string) string { return "  " + ansi.Muted(fmt.Sprintf("%-10s", l)) }
	if err != nil {
		if infoview.NoQuota(err) {
			return ""
		}
		return label("quota") + ansi.Dimmed("unavailable ("+err.Error()+")") + "\n"
	}
	if q == nil {
		return ""
	}
	var b strings.Builder
	head := infoview.Header(provider, q)
	if note := infoview.StaleNote(q, now); note != "" {
		head += ansi.Dimmed(note)
	}
	b.WriteString(label("quota") + head + "\n")
	for _, w := range q.Windows {
		short, scope := infoview.WindowLabel(w)
		line := label(short) + quotaBar(w.UsedPercent) + " " + infoview.Percent(w.UsedPercent)
		if d := infoview.WindowDetail(scope, w.ResetsAt, now); d != "" {
			line += "   " + ansi.Dimmed(d)
		}
		b.WriteString(line + "\n")
	}
	if bal := q.Balance; bal != nil {
		line := label("balance") + infoview.Balance(bal)
		if !bal.ResetsAt.IsZero() {
			line += "   " + ansi.Dimmed("resets in "+infoview.Until(bal.ResetsAt, now))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// quotaBar is the shared bar, colored by its usage level.
func quotaBar(used float64) string {
	bar := infoview.Bar(used)
	switch infoview.Level(used) {
	case infoview.LevelCritical:
		return ansi.Err(bar)
	case infoview.LevelHigh:
		return ansi.Warn(bar)
	}
	return bar
}
