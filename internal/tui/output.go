package tui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/components"
)

// beginSection inserts a Spacer(1) when the logical section kind changes
// (mirrors PI's chatContainer.addChild(new Spacer(1)) between sections) and
// closes the live streaming block so the next content starts fresh at the end
// of history — keeping the scrollback strictly chronological. Caller holds mu.
func (t *TUI) beginSection(kind string) {
	if t.lastKind != "" && t.lastKind != kind && t.history.Len() > 0 {
		t.history.Add(components.NewSpacer(1))
	}
	t.lastKind = kind
	// Any section boundary detaches the live markdown block: subsequent text
	// (e.g. the post-tool ReAct response) must create a NEW block appended at
	// the end, never append to a block that now sits above tool output.
	if kind != "text" {
		t.liveMD = nil
	}
}

// addRaw appends a pre-styled block (notices, separators). Treated as its own
// section so it is spaced from surrounding content.
func (t *TUI) addRaw(text string) *components.RawBlock {
	return t.addSection("notice", text)
}

// addSection inserts a section boundary then appends a pre-styled block.
func (t *TUI) addSection(kind, text string) *components.RawBlock {
	b := components.NewRawBlock(text)
	t.mu.Lock()
	t.beginSection(kind)
	t.history.Add(b)
	t.mu.Unlock()
	t.tui.RequestRender(false)
	return b
}

// addMarkdown appends a source-backed markdown block (re-renders on resize).
func (t *TUI) addMarkdown(source string) *components.Markdown {
	b := components.NewMarkdown(source)
	t.mu.Lock()
	t.history.Add(b)
	t.mu.Unlock()
	t.tui.RequestRender(false)
	return b
}

// showWarn prints a warning block to the scrollback.
func (t *TUI) showWarn(msg string) {
	t.addRaw(ansi.Warn("⚠ " + msg))
}

// refreshBadges reloads the footer status counts (MCP connected, schedule jobs)
// from the server. Cheap read-only calls; run at connect time and after changes.
func (t *TUI) refreshBadges() {
	if t.client == nil {
		return
	}
	// MCP: count connected servers (counted first, published under viewMu).
	n := 0
	if statuses, err := t.client.GetMCPStatus(); err == nil {
		for _, s := range statuses {
			if s.Connected {
				n++
			}
		}
	}
	t.withView(func() { t.mcpConnected = n })
	t.refreshScheduleBadge()
}

// refreshScheduleBadge reloads just the schedule count (only meaningful when the
// engine runs). Split out so a Schedule/ScheduleDelete tool result can refresh
// the badge without also re-querying MCP status.
func (t *TUI) refreshScheduleBadge() {
	n := 0
	// Count only the schedules owned by this session — the ones that will actually
	// fire here. A schedule only ever runs in its owner session, so a global count
	// would be dishonest for this session's footer.
	if t.client != nil && t.schedulerOn {
		if jobs, err := t.client.GetSchedules(t.view().sessionID); err == nil {
			n = len(jobs)
		}
	}
	t.withView(func() { t.scheduleJobs = n })
}

// updateInfo refreshes the info and footer status lines.
func (t *TUI) updateInfo() {
	cwd, _ := os.Getwd()
	loc := shortenPath(cwd)
	if branch := gitBranch(cwd); branch != "" {
		loc += " (" + branch + ")"
	}
	v := t.view()
	name := v.sessionName
	if name == "" {
		name = "No session"
	}
	// Turn progress: only meaningful while the agent is actively working — set
	// directly from each loop_start event's own 0-based Loop index (+1 for
	// this 1-based display; see events.go) and hidden again on turn_end.
	// maxIterations > 0 guard covers a session whose info hasn't loaded
	// max_iterations yet (e.g. very first render before autoConnect resolves).
	turn := ""
	if t.isSpinning() && v.maxIterations > 0 {
		turn = fmt.Sprintf(" (%d/%d)", v.currTurn, v.maxIterations)
	}
	queue := ""
	if v.queueCount > 0 {
		queue = fmt.Sprintf(" [%d queued]", v.queueCount)
	}
	t.info.SetText(ansi.Dimmed(fmt.Sprintf("%s • %s%s%s", loc, name, turn, queue)))
	// The channels get a line of their own above, only while there are any.
	if t.channelsLine == nil {
		// a TUI built without the full layout (tests): nothing to update
	} else if full, short := statusLineTexts(v.subagentNames, v.channelInfos); full != "" {
		t.channelsLine.SetTexts(ansi.Dimmed(full), ansi.Dimmed(short))
	} else {
		t.channelsLine.SetText("")
	}

	if v.model == "" {
		t.footer.SetText("")
		t.tui.RequestRender(false)
		return
	}
	thinking := ""
	if v.thinking != "" && v.thinking != "off" {
		thinking = " (" + v.thinking + ")"
	}
	st := t.statsSnapshot()
	cache := ""
	if st.cacheRead > 0 || st.cacheWrite > 0 {
		cache = fmt.Sprintf(" R%s W%s", compactNum(st.cacheRead), compactNum(st.cacheWrite))
	}
	price := fmt.Sprintf("$%.3f", st.cost)
	if v.isSubscription {
		price += " (sub)"
	}
	stats := ansi.Dimmed(fmt.Sprintf(
		"↑%s ↓%s%s %s %.1f%%/%s %s%s",
		compactNum(st.input),
		compactNum(st.output),
		cache,
		price,
		st.contextPct*100,
		compactNum(st.contextWin),
		v.model,
		thinking,
	))
	t.footer.SetText(stats + t.statusBadges())
	t.tui.RequestRender(false)
}

// statusBadges renders the chartreuse status badges appended to the footer:
// connected MCP servers, and (when --with-scheduler is on with jobs) the scheduler.
// Each is shown only when it has something to report.
func (t *TUI) statusBadges() string {
	var parts []string
	t.viewMu.Lock()
	mcp, jobs := t.mcpConnected, t.scheduleJobs
	t.viewMu.Unlock()
	if mcp > 0 {
		word := "mcps"
		if mcp == 1 {
			word = "mcp"
		}
		parts = append(parts, badge(fmt.Sprintf("%d %s", mcp, word)))
	}
	if t.schedulerOn && jobs > 0 {
		word := "schedules"
		if jobs == 1 {
			word = "schedule"
		}
		parts = append(parts, badge(fmt.Sprintf("%d %s", jobs, word)))
	}
	if len(parts) == 0 {
		return ""
	}
	// Separate the stats line from the badges with a dim bullet; badges joined by spaces.
	return ansi.Dimmed(" • ") + strings.Join(parts, " ")
}

// badge renders bracketed text fully dimmed, matching the rest of the footer,
// e.g. [2 mcps].
func badge(text string) string {
	return ansi.Dimmed("[" + text + "]")
}

// ── Formatting helpers (ported from frontend/tui) ──────────────────────────

// toolStyle returns the color and icon for a tool header. Built-in tools each
// get a distinctive icon; the generic gear (⚙) is reserved for MCP/extension
// tools we don't recognize, so a glance tells built-in from external.
func toolStyle(name string) (colorFn func(string) string, icon string) {
	switch name {
	case "Bash":
		return ansi.Accent, "$" // classic shell prompt (distinct from the user's ❯)
	case "Read":
		return ansi.Accent, "≡" // triple bar (narrow): text content of a file
	case "Write":
		return ansi.Accent, "✚" // heavy plus: create/write a file
	case "Edit":
		return ansi.Accent, "✎" // pencil: editing
	case "Fetch":
		return ansi.Accent, "↓" // down arrow: fetching/downloading
	case "Skill":
		return ansi.Accent, "✦" // star: skill
	case "Subagent":
		return ansi.Accent, "⊕" // circled plus: spawn a sub-agent
	case "MemoWrite", "MemoSearch", "MemoDelete":
		return ansi.Accent, "✳" // asterisk: memory note
	case "Schedule", "ScheduleList", "ScheduleDelete":
		return ansi.Accent, "◷" // clock: cron-scheduled prompt management
	case "ColleagueList", "ColleagueAsk":
		return ansi.Accent, "⚇" // two figures: another agent like this one
	case "WebSearch":
		return ansi.Accent, "◌" // diana/objetivo (U+25CC): live web search
	case "SessionInfo":
		return ansi.Accent, "ℹ" // info symbol (U+2139): snapshot of session identity/config, not an action
	}
	if isChannelTool(name) {
		return ansi.Accent, "⌁" // a connection: a tool an input channel brought (SlackPost, …)
	}
	return ansi.Accent, "◈" // diamond: generic MCP/external tool
}

// channelToolPrefixes are the tool-name prefixes of the input channels this
// TUI knows: a channel's tools are all named after its type (enforced when a
// session binds the channel: agent.prepareChannels), so the prefix is a fact,
// not a guess. Add a prefix with each new channel type.
var channelToolPrefixes = []string{"Slack", "Telegram"}

func isChannelTool(name string) bool {
	for _, p := range channelToolPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// mcpDisplayName converts an MCP tool name from its internal wire format
// (mcp__<namespace>__<ToolName>) to a human-readable display form
// (namespace.ToolName). Only names starting with "mcp__" are transformed;
// all others are returned unchanged. This is purely a TUI rendering concern —
// the internal name is never modified.
//
//	mcp__ext__WebSearch  →  ext.WebSearch
//	mcp__x__MemoWrite    →  x.MemoWrite
//	mcp__myserver__Get   →  myserver.Get
//	Bash                 →  Bash  (unchanged)
func mcpDisplayName(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	// Strip leading "mcp__" and replace the next "__" with "."
	rest := strings.TrimPrefix(name, "mcp__")
	if i := strings.Index(rest, "__"); i >= 0 {
		return rest[:i] + "." + rest[i+2:]
	}
	return rest // "mcp__orphan" — no second __ — just drop the prefix
}

// stripANSI removes every escape sequence from s, for text that is summarized on one line (a tool's
// result). It delegates to ansi.StripEscapes, which knows the whole shape of an escape sequence: the
// recognizer this used to run (ansi.ExtractAnsiCode) only knows the few the TUI itself emits, so a
// tool output carrying CSI 6n or a DCS string went through it untouched.
func stripANSI(s string) string { return ansi.StripEscapes(s) }

// collapseWhitespace flattens a multi-line string into a single line: runs of
// whitespace (including newlines and tabs) become a single space, trimmed. Used
// to render a tool error as a one-line summary.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// unescapeArgs turns the JSON-escaped whitespace inside tool-call args into real
// characters, so a multi-line string value (e.g. a markdown comment body) shows
// as actual line breaks instead of literal "\n". The RawBlock then renders those
// lines faithfully. A literal backslash ("\\") is preserved. Only \n and \t are
// unescaped — other JSON escapes are left as-is (rare in displayed args).
func unescapeArgs(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func formatDur(ms float64) string {
	switch {
	case ms >= 1000:
		return fmt.Sprintf("%.1fs", ms/1000)
	case ms >= 1:
		return fmt.Sprintf("%.0fms", ms)
	default:
		// Sub-millisecond: show "<1ms" rather than "0ms" so fast tools still read
		// as having run (and the [time] tag stays consistent across calls).
		return "<1ms"
	}
}

func compactNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func gitBranch(cwd string) string {
	data, err := os.ReadFile(cwd + "/.git/HEAD")
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(data))
	if strings.HasPrefix(ref, "ref: refs/heads/") {
		return strings.TrimPrefix(ref, "ref: refs/heads/")
	}
	return ""
}

func shortenPath(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// shortModel strips provider prefixes and the redundant "claude-" vendor tag so
// "claude-oauth/claude-opus-4-6" → "opus-4-6" and "openai/gpt-4o" → "gpt-4o".
func shortModel(model string) string {
	if model == "" {
		return ""
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	model = strings.TrimPrefix(model, "claude-")
	return model
}

// relativeTime renders a timestamp as a compact "time ago" string: "just now",
// "5m ago", "2h ago", "3d ago", or a short date ("Jun 20") beyond a week.
// Returns "" for the zero time (e.g. a session that never recorded activity).
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}

// statsSnapshot copies the footer's token stats under t.mu: they are written
// by the SSE consumer, by a session switch and by /reset, each on its own
// goroutine, while updateInfo reads them on yet another.
func (t *TUI) statsSnapshot() tokensInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stats
}

// sessionView is a consistent copy of what the status lines show.
type sessionView struct {
	sessionID, sessionName, model, thinking string
	isSubscription                          bool
	maxIterations, currTurn, queueCount     int
	channelInfos                            []client.ChannelInfo
	subagentNames                           []string
}

// view copies the session state under viewMu (see TUI.viewMu).
func (t *TUI) view() sessionView {
	t.viewMu.Lock()
	defer t.viewMu.Unlock()
	return sessionView{
		sessionID: t.sessionID, sessionName: t.sessionName, model: t.model, thinking: t.thinking,
		isSubscription: t.isSubscription, maxIterations: t.maxIterations,
		currTurn: t.currTurn, queueCount: t.queueCount,
		channelInfos:  append([]client.ChannelInfo(nil), t.channelInfos...),
		subagentNames: append([]string(nil), t.subagentNames...),
	}
}

// withView runs fn with viewMu held: every write of the session state goes
// through it (or setSessionName).
func (t *TUI) withView(fn func()) {
	t.viewMu.Lock()
	defer t.viewMu.Unlock()
	fn()
}

// switchView sets the whole session state at once (a switch to another session).
// subagents are the sub-agents the session already has running (its detail).
func (t *TUI) switchView(id, model, thinking string, maxIter int, chans []client.ChannelInfo, subagents []string) {
	t.withView(func() {
		t.sessionID, t.model, t.thinking, t.maxIterations, t.channelInfos = id, model, thinking, maxIter, chans
		t.subagentNames = append([]string(nil), subagents...)
	})
}

// applySubagentEvent applies a subagent_start (add, if absent) or
// subagent_stop (remove) of the stream whose context is ctx. It does nothing
// when that stream was replaced: the check is made under viewMu, and a switch
// cancels the old stream BEFORE its switchView takes the same lock, so a late
// event of the old session can never touch the new session's list.
func (t *TUI) applySubagentEvent(ctx context.Context, evt client.Event) {
	t.withView(func() {
		if ctx.Err() != nil || evt.Name == "" {
			return
		}
		i := slices.Index(t.subagentNames, evt.Name)
		switch evt.Type {
		case "subagent_start":
			if i < 0 {
				t.subagentNames = append(t.subagentNames, evt.Name)
			}
		case "subagent_stop":
			if i >= 0 {
				t.subagentNames = slices.Delete(t.subagentNames, i, i+1)
			}
		}
	})
	t.updateInfo()
}

// queued is how many prompts wait behind the running turn (under viewMu).
func (t *TUI) queued() int {
	t.viewMu.Lock()
	defer t.viewMu.Unlock()
	return t.queueCount
}
