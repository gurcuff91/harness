package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
	"github.com/gurcuff91/harness/internal/transport/tui/components"
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
	// MCP: count connected servers.
	t.mcpConnected = 0
	if data, err := t.client.GetMCPStatus(); err == nil {
		var statuses []struct {
			Connected bool `json:"connected"`
		}
		if json.Unmarshal(data, &statuses) == nil {
			for _, s := range statuses {
				if s.Connected {
					t.mcpConnected++
				}
			}
		}
	}
	t.refreshScheduleBadge()
}

// refreshScheduleBadge reloads just the schedule count (only meaningful when the
// engine runs). Split out so a Schedule/ScheduleDelete tool result can refresh
// the badge without also re-querying MCP status.
func (t *TUI) refreshScheduleBadge() {
	if t.client == nil || !t.schedulerOn {
		t.scheduleJobs = 0
		return
	}
	t.scheduleJobs = 0
	// Count only the schedules owned by this session — the ones that will actually
	// fire here. A schedule only ever runs in its owner session, so a global count
	// would be dishonest for this session's footer.
	if data, err := t.client.GetSchedules(t.sessionID); err == nil {
		var jobs []json.RawMessage
		if json.Unmarshal(data, &jobs) == nil {
			t.scheduleJobs = len(jobs)
		}
	}
}

// updateInfo refreshes the info and footer status lines.
func (t *TUI) updateInfo() {
	cwd, _ := os.Getwd()
	loc := shortenPath(cwd)
	if branch := gitBranch(cwd); branch != "" {
		loc += " (" + branch + ")"
	}
	name := t.sessionName
	if name == "" {
		name = "No session"
	}
	// Turn progress: only meaningful while the agent is actively working — shown
	// from turn_start (reset to 0, then incremented per loop_start) and hidden
	// again on turn_end. maxTurns > 0 guard covers a session whose info hasn't
	// loaded max_turns yet (e.g. very first render before autoConnect resolves).
	turn := ""
	if t.isSpinning() && t.maxTurns > 0 {
		turn = fmt.Sprintf(" (%d/%d)", t.currTurn, t.maxTurns)
	}
	queue := ""
	if t.queueCount > 0 {
		queue = fmt.Sprintf(" [%d queued]", t.queueCount)
	}
	t.info.SetText(ansi.Dimmed(fmt.Sprintf("%s • %s%s%s", loc, name, turn, queue)))

	if t.model == "" {
		t.footer.SetText("")
		t.tui.RequestRender(false)
		return
	}
	thinking := ""
	if t.thinking != "" && t.thinking != "off" {
		thinking = " (" + t.thinking + ")"
	}
	cache := ""
	if t.stats.cacheRead > 0 || t.stats.cacheWrite > 0 {
		cache = fmt.Sprintf(" R%s W%s", compactNum(t.stats.cacheRead), compactNum(t.stats.cacheWrite))
	}
	price := fmt.Sprintf("$%.3f", t.stats.cost)
	if t.isSubscription {
		price += " (sub)"
	}
	stats := ansi.Dimmed(fmt.Sprintf(
		"↑%s ↓%s%s %s %.1f%%/%s %s%s",
		compactNum(t.stats.input),
		compactNum(t.stats.output),
		cache,
		price,
		t.stats.contextPct*100,
		compactNum(t.stats.contextWin),
		t.model,
		thinking,
	))
	t.footer.SetText(stats + t.statusBadges())
	t.tui.RequestRender(false)
}

// statusBadges renders the chartreuse status badges appended to the footer:
// connected MCP servers, and (when --scheduler is on with jobs) the scheduler.
// Each is shown only when it has something to report.
func (t *TUI) statusBadges() string {
	var parts []string
	if t.mcpConnected > 0 {
		word := "mcps"
		if t.mcpConnected == 1 {
			word = "mcp"
		}
		parts = append(parts, badge(fmt.Sprintf("%d %s", t.mcpConnected, word)))
	}
	if t.schedulerOn && t.scheduleJobs > 0 {
		word := "schedules"
		if t.scheduleJobs == 1 {
			word = "schedule"
		}
		parts = append(parts, badge(fmt.Sprintf("%d %s", t.scheduleJobs, word)))
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

// ── Formatting helpers (ported from transport/tui) ──────────────────────────

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
	default:
		return ansi.Accent, "⎔" // technical hexagon: generic MCP/extension tool
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if code, length := ansi.ExtractAnsiCode(s, i); length > 0 {
			_ = code
			i += length
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

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

// relativeTime renders an RFC3339 timestamp as a compact "time ago" string:
// "just now", "5m ago", "2h ago", "3d ago", or a short date ("Jun 20") beyond a
// week. Returns "" if the timestamp can't be parsed.
func relativeTime(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// Some encoders include sub-second precision / timezone offsets; try the
		// nano variant before giving up.
		if t, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return ""
		}
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

func intFromMap(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func floatFromMap(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	if f, ok := v.(float64); ok {
		return f, true
	}
	return 0, false
}
