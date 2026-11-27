package infoview

import (
	"fmt"
	"strings"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// The plain-text /info and /context panels, for the frontends whose output is
// a Markdown code fence (ACP, Telegram): fixed-width columns only line up in a
// monospace font. No colors: a window near its limit is marked with words
// (" ⚠ high" from 70 %, " ⚠ almost out" above 90 %).

// PlainWidth is the label column of the plain panels.
const PlainWidth = 16

// Info is what the /info panel shows, whatever the frontend got it from (the
// HTTP API, or a channel's agent.SessionView).
type Info struct {
	Version       string
	ID, Name      string
	Model         string
	Thinking      string
	MaxIterations int
	Stats         types.SessionStats
	Channels      []client.ChannelInfo
	// Subagents are the names of the sub-agents running now (a row only when any).
	Subagents     []string
	Busy          bool
	QueueDepth    int
	MCPConnected  int
	ScheduleCount int
	OS, Arch      string
	// Quota / QuotaErr: the session provider's quota reading (see QuotaRows).
	Quota    *types.Quota
	QuotaErr error
}

// Context is what the /context panel shows.
type Context struct {
	System, Tools, Conversation   int
	EstimatedTotal, LastRealTotal int
	ContextWindow, FreeSpace      int
}

func row(label, value string) string { return fmt.Sprintf("%-*s%s\n", PlainWidth, label, value) }

// InfoPlain renders the /info panel as a fenced code block: identity, model,
// usage, the quota of the session's provider, environment and channels. No
// terminal row: the plain panel is for frontends that do not run in one.
func InfoPlain(in Info, now time.Time) string {
	var b strings.Builder
	b.WriteString("📊 Session info\n")
	b.WriteString(strings.Repeat("━", 28) + "\n")
	b.WriteString(row("harness", in.Version))
	name := in.Name
	if name == "" && len(in.ID) >= 8 {
		name = in.ID[:8]
	}
	b.WriteString(row("session", name))
	b.WriteString(row("id", in.ID))
	b.WriteByte('\n')
	b.WriteString(row("model", in.Model))
	thinking := in.Thinking
	if thinking == "" {
		thinking = "off"
	}
	b.WriteString(row("thinking", thinking))
	b.WriteString(row("iters", fmt.Sprintf("max %d", in.MaxIterations)))
	st := in.Stats
	if st.ContextWindow > 0 {
		b.WriteString(row("context", fmt.Sprintf("%.1f%% of %s tokens", st.ContextUsage*100, CompactNum(st.ContextWindow))))
	}
	b.WriteByte('\n')
	b.WriteString(row("tokens", fmt.Sprintf("↑%s ↓%s", CompactNum(st.InputTokens), CompactNum(st.OutputTokens))))
	if st.CacheRead > 0 || st.CacheWrite > 0 {
		b.WriteString(row("cache", fmt.Sprintf("R%s W%s", CompactNum(st.CacheRead), CompactNum(st.CacheWrite))))
	}
	b.WriteString(row("cost", fmt.Sprintf("$%.4f", st.CostUSD)))
	if sec := QuotaRows(ProviderOf(in.Model), in.Quota, in.QuotaErr, now); sec != "" {
		b.WriteByte('\n')
		b.WriteString(sec)
	}
	b.WriteByte('\n')
	if in.OS != "" {
		b.WriteString(row("system", in.OS+"/"+in.Arch))
	}
	b.WriteString(row("mcps", fmt.Sprintf("%d connected", in.MCPConnected)))
	b.WriteString(row("schedules", fmt.Sprintf("%d", in.ScheduleCount)))
	if len(in.Subagents) > 0 {
		b.WriteString(row("subagents", strings.Join(in.Subagents, ", ")))
	}
	for _, g := range ChannelGroups(in.Channels) {
		b.WriteString(row(g.Type, strings.Join(g.Names, ", ")))
	}
	if in.Busy {
		queued := ""
		if in.QueueDepth > 0 {
			queued = fmt.Sprintf(" (%d queued)", in.QueueDepth)
		}
		b.WriteString("\n⚙ busy" + queued + "\n")
	}
	return Fence(b.String())
}

// QuotaRows renders the quota rows of the plain panel. "" when the provider
// has no quota (a 404 or ErrNoQuota-like error, or nothing fetched); any
// other failure is one "unavailable (...)" row.
func QuotaRows(provider string, q *types.Quota, err error, now time.Time) string {
	if err != nil {
		if NoQuota(err) {
			return ""
		}
		return row("quota", "unavailable ("+err.Error()+")")
	}
	if q == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(row("quota", Header(provider, q)+StaleNote(q, now)))
	for _, w := range q.Windows {
		label, scope := WindowLabel(w)
		v := Bar(w.UsedPercent) + " " + Percent(w.UsedPercent)
		if d := WindowDetail(scope, w.ResetsAt, now); d != "" {
			v += "   " + d
		}
		switch Level(w.UsedPercent) {
		case LevelCritical:
			v += "  ⚠ almost out"
		case LevelHigh:
			v += "  ⚠ high"
		}
		b.WriteString(row(label, v))
	}
	if bal := q.Balance; bal != nil {
		v := Balance(bal)
		if !bal.ResetsAt.IsZero() {
			v += "   resets in " + Until(bal.ResetsAt, now)
		}
		b.WriteString(row("balance", v))
	}
	return b.String()
}

// ContextPlain renders the /context panel as a fenced code block.
func ContextPlain(c Context) string {
	cn := CompactNum
	sub := func(label, value string) string { return fmt.Sprintf("  %-*s%s\n", PlainWidth-2, label, value) }
	var b strings.Builder
	b.WriteString("📐 Context breakdown\n")
	b.WriteString(strings.Repeat("━", 28) + "\n")
	b.WriteString(row("system prompt", cn(c.System)+" tokens"))
	b.WriteString(row("tools", cn(c.Tools)+" tokens"))
	b.WriteString(row("conversation", cn(c.Conversation)+" tokens"))
	b.WriteString(strings.Repeat("─", 28) + "\n")
	b.WriteString(row("estimated", "~"+cn(c.EstimatedTotal)+" tokens"))
	if c.LastRealTotal > 0 && c.ContextWindow > 0 {
		pct := float64(c.LastRealTotal) / float64(c.ContextWindow) * 100
		b.WriteString(row("actual", cn(c.LastRealTotal)+fmt.Sprintf(" tokens (%.1f%%)", pct)))
		b.WriteString(sub("free", cn(c.FreeSpace)+" of "+cn(c.ContextWindow)))
	} else {
		b.WriteString("(no turn yet)\n")
	}
	return Fence(b.String())
}

// Fence wraps text in a Markdown code fence (monospace), trimming the trailing
// newline so the closing ``` is on its own line.
func Fence(text string) string {
	return "```\n" + strings.TrimRight(text, "\n") + "\n```\n"
}

// CompactNum formats a count with k/M suffixes ("1.2k", "3M").
func CompactNum(n int) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	case n >= 1_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000), ".0") + "k"
	}
	return fmt.Sprintf("%d", n)
}
