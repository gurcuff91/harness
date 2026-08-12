package acp

import (
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/client"
)

// buildConfigOptions returns the session config options this transport
// advertises: the active model (category "model" — ACP's semantic category
// for a model selector; "model_config" is reserved for model-related
// PARAMETERS like context size, not the selector itself) and the thinking
// level (category "thought_level", ACP's dedicated category for a
// reasoning-level selector).
//
// The currentValue of each is read from THE SESSION (GetSession), not the
// global settings default — ACP config options are per-session (each ACP
// session carries its own model/thinking), and a session's /model or
// /thinking command changes only that session, never the global default. (It
// used to read settings.ActiveModel/ThinkingLevel, which only happened to be
// right while the session command also wrote the global default — a coupling
// since removed.)
func buildConfigOptions(c *client.Client, sessionID string) ([]sessionConfigOption, error) {
	sess, err := c.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("acp: get session: %w", err)
	}
	models, err := c.ListModels()
	if err != nil {
		return nil, fmt.Errorf("acp: list models: %w", err)
	}

	// m.Model is already the full "provider/model" identifier (see
	// GET /api/models) — not a bare model name needing the provider prefix.
	modelValues := make([]sessionConfigOptionValue, 0, len(models))
	for _, m := range models {
		modelValues = append(modelValues, sessionConfigOptionValue{Value: m.Model, Name: m.Model})
	}

	thinkingValues := []sessionConfigOptionValue{
		{Value: "off", Name: "Off"},
		{Value: "low", Name: "Low"},
		{Value: "medium", Name: "Medium"},
		{Value: "high", Name: "High"},
		{Value: "xhigh", Name: "XHigh"},
	}

	thinking := sess.Thinking
	if thinking == "" {
		thinking = "off"
	}

	return []sessionConfigOption{
		{
			ID:           "model",
			Category:     "model",
			Name:         "Model",
			Type:         "select",
			CurrentValue: sess.Model,
			Options:      modelValues,
		},
		{
			ID:           "thinking",
			Category:     "thought_level",
			Name:         "Thinking",
			Type:         "select",
			CurrentValue: thinking,
			Options:      thinkingValues,
		},
	}, nil
}

// commandsExcludedFromACP are session commands Harness exposes over the HTTP
// API (client.ListCommands) that this transport deliberately does NOT
// advertise in available_commands_update — for two different reasons:
//
//   - "model"/"thinking" are already exposed as native ACP configOptions
//     selectors (session/set_config_option) — a proper dropdown with
//     validated values. Advertising them AGAIN as slash commands would mean
//     two different, redundant ways to do the same thing in Zed's UI, with
//     the slash-command path being strictly worse (free-text value, no
//     autocomplete, no validation against the actual option list).
//   - "rename"/"reset" have no meaningful ACP equivalent at all: the client
//     owns how it displays a session/thread's name (no protocol channel
//     exists for the agent to push one live), and wiping Harness's own
//     history via reset would desync from whatever Zed already rendered on
//     screen from the session/update notifications already sent — there's
//     no "clear this thread" mechanism in ACP v1 to keep the two in sync.
//     Neither is executed specially by handlePrompt's executableCommand
//     either (see methods.go) — they were never wired to do anything ACP-side
//     beyond appearing in this list, so hiding them here is the complete fix,
//     not just half of one.
//
// Everything else Harness exposes ("compact" and every "skill:<name>") has
// no such equivalent and stays — both are also the only two
// executableCommand actually executes (see methods.go), which is the
// property that keeps this list truthful: every command it advertises here
// really does something distinct from a plain prompt when invoked.
var commandsExcludedFromACP = map[string]bool{
	"model":    true,
	"thinking": true,
	"rename":   true,
	"reset":    true,
}

// buildAvailableCommands translates the session's dynamic command set
// (built-ins + discovered skills, from GET /api/sessions/{id}/commands) into
// ACP's available_commands_update shape, excluding whatever
// commandsExcludedFromACP already covers.
func buildAvailableCommands(c *client.Client, sessionID string) ([]availableCommand, error) {
	defs, err := c.ListCommands(sessionID)
	if err != nil {
		return nil, fmt.Errorf("acp: list commands: %w", err)
	}
	out := make([]availableCommand, 0, len(defs)+2)
	for _, d := range defs {
		if commandsExcludedFromACP[d.Name] {
			continue
		}
		out = append(out, availableCommand{
			Name:        d.Name,
			Description: d.Description,
			Input:       commandInputHint(d),
		})
	}
	// "info" and "context" are NOT session commands in Harness's command
	// system (ListCommands only exposes rename/thinking/model/compact/reset
	// + skills) — they're standalone GET endpoints (/api/sessions/{id}/info
	// and /context) that Slack's /info and /context call directly. But from
	// the ACP user's perspective they're still slash commands worth typing
	// and seeing in Zed's command picker, so append them here as read-only
	// queries with no params. They're handled inline in handlePrompt (no
	// ExecCommand, no event stream — just an API call + formatted chunk).
	out = append(out,
		availableCommand{
			Name:        "info",
			Description: "Show session info (model, thinking, tokens, cost, MCPs)",
		},
		availableCommand{
			Name:        "context",
			Description: "Show context window breakdown (system, tools, conversation, free space)",
		},
	)
	return out, nil
}

// commandInputHint builds a human-readable input hint from a command's
// declared params — e.g. "level (off|low|medium|high|xhigh)" for /thinking,
// "prompt" for a skill command — or nil if the command takes no params.
func commandInputHint(d client.CommandDef) *availableCommandInput {
	if len(d.Params) == 0 {
		return nil
	}
	hint := ""
	for i, p := range d.Params {
		if i > 0 {
			hint += ", "
		}
		hint += p.Name
		if len(p.Values) > 0 {
			hint += " ("
			for j, v := range p.Values {
				if j > 0 {
					hint += "|"
				}
				hint += v
			}
			hint += ")"
		}
	}
	return &availableCommandInput{Hint: hint}
}

// compactNum formats a number with k/M suffixes for compact display.
// Mirrors the same logic Slack's formatInfo/formatContext use.
func compactNum(n int64) string {
	switch {
	case n >= 1_000_000:
		f := fmt.Sprintf("%.1f", float64(n)/1_000_000)
		return strings.TrimSuffix(f, ".0") + "M"
	case n >= 1_000:
		f := fmt.Sprintf("%.1f", float64(n)/1_000)
		return strings.TrimSuffix(f, ".0") + "k"
	default:
		return fmt.Sprintf("%d", n)
	}
}

// infoContextFenceWidth is the fixed column width label rows are padded to
// in both formatInfoPlain and formatContextPlain — wide enough for the
// longest label ("system prompt") plus a couple of spaces before the value.
const infoContextFenceWidth = 16

// formatInfoPlain renders SessionInfo as a fenced code block so Zed renders
// it in a monospace font — WITHOUT this, the padded label/value columns
// below only line up in the raw string; Zed's agent_message_chunk renders
// plain text as Markdown in a proportional font, where fixed-width padding
// is invisible (e.g. "harness" and "thinking" occupy different pixel widths,
// so aligned columns in the source string look ragged on screen — this was
// a real, reported rendering bug). Slack's formatInfo (slack.go) has always
// wrapped its output in ``` for the exact same reason; this mirrors that,
// adding a title + separator line since the code fence also drops Markdown
// bold, which Slack's version used for the title instead.
func formatInfoPlain(info *client.SessionInfo) string {
	if info == nil {
		return "⚠️ No session info available.\n"
	}
	sess := info.Session
	stats := sess.Stats

	row := func(label, value string) string {
		return fmt.Sprintf("%-*s%s\n", infoContextFenceWidth, label, value)
	}

	var b strings.Builder
	b.WriteString("📊 Session info\n")
	b.WriteString(strings.Repeat("━", 28) + "\n")
	b.WriteString(row("harness", info.Version))
	name := sess.Name
	if name == "" {
		name = sess.ID[:8]
	}
	b.WriteString(row("session", name))
	b.WriteByte('\n')
	b.WriteString(row("model", sess.Model))
	thinking := sess.Thinking
	if thinking == "" {
		thinking = "off"
	}
	b.WriteString(row("thinking", thinking))
	b.WriteString(row("iters", fmt.Sprintf("max %d", sess.MaxIterations)))
	if stats.ContextWindow > 0 {
		b.WriteString(row("context", fmt.Sprintf("%.1f%% of %s tokens",
			stats.ContextUsage*100, compactNum(int64(stats.ContextWindow)))))
	}
	b.WriteByte('\n')
	b.WriteString(row("tokens", fmt.Sprintf("↑%s ↓%s",
		compactNum(int64(stats.InputTokens)), compactNum(int64(stats.OutputTokens)))))
	if stats.CacheRead > 0 || stats.CacheWrite > 0 {
		b.WriteString(row("cache", fmt.Sprintf("R%s W%s",
			compactNum(int64(stats.CacheRead)), compactNum(int64(stats.CacheWrite)))))
	}
	b.WriteString(row("cost", fmt.Sprintf("$%.4f", stats.CostUSD)))
	b.WriteByte('\n')
	b.WriteString(row("mcps", fmt.Sprintf("%d connected", info.MCPConnected)))
	if info.ScheduleCount > 0 {
		b.WriteString(row("schedules", fmt.Sprintf("%d", info.ScheduleCount)))
	}
	if info.Busy {
		queued := ""
		if info.QueueDepth > 0 {
			queued = fmt.Sprintf(" (%d queued)", info.QueueDepth)
		}
		b.WriteString("\n⚙ busy" + queued + "\n")
	}
	return fence(b.String())
}

// formatContextPlain renders ContextBreakdown as a fenced code block — same
// monospace-alignment reasoning as formatInfoPlain's doc comment.
func formatContextPlain(bd *client.ContextBreakdown) string {
	if bd == nil {
		return "⚠️ No context data available.\n"
	}
	win := bd.ContextWindow
	cn := func(n int) string { return compactNum(int64(n)) }

	row := func(label, value string) string {
		return fmt.Sprintf("%-*s%s\n", infoContextFenceWidth, label, value)
	}
	sub := func(label, value string) string {
		return fmt.Sprintf("  %-*s%s\n", infoContextFenceWidth-2, label, value)
	}

	var b strings.Builder
	b.WriteString("📐 Context breakdown\n")
	b.WriteString(strings.Repeat("━", 28) + "\n")
	b.WriteString(row("system prompt", cn(bd.System)+" tokens"))
	b.WriteString(row("tools", cn(bd.Tools)+" tokens"))
	b.WriteString(row("conversation", cn(bd.Conversation)+" tokens"))
	b.WriteString(strings.Repeat("─", 28) + "\n")
	b.WriteString(row("estimated", "~"+cn(bd.EstimatedTotal)+" tokens"))
	if bd.LastRealTotal > 0 && win > 0 {
		pct := float64(bd.LastRealTotal) / float64(win) * 100
		b.WriteString(row("actual", cn(bd.LastRealTotal)+fmt.Sprintf(" tokens (%.1f%%)", pct)))
		b.WriteString(sub("free", cn(bd.FreeSpace)+" of "+cn(win)))
	} else {
		b.WriteString("(no turn yet)\n")
	}
	return fence(b.String())
}

// fence wraps text in a Markdown fenced code block, trimming the trailing
// newline first so the closing "```" lands on its own clean line instead of
// leaving a blank line before it.
func fence(text string) string {
	var b strings.Builder
	b.WriteString("```\n")
	b.WriteString(strings.TrimRight(text, "\n"))
	b.WriteString("\n```\n")
	return b.String()
}
