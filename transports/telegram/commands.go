package telegram

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/gurcuff91/harness/client"
)

// botCommands is the command menu registered with Telegram (setMyCommands) and
// the set this transport handles. Descriptions show in the "/" suggestion list.
var botCommands = []BotCommand{
	{Command: "stop", Description: "🛑 Stop the current work"},
	{Command: "compact", Description: "🗜 Summarize & compact the conversation"},
	{Command: "reset", Description: "🔄 Wipe history & stats, start fresh"},
	{Command: "info", Description: "📊 Session & model info"},
	{Command: "context", Description: "📐 Context window breakdown"},
	{Command: "thinking", Description: "🧠 Change thinking level"},
	{Command: "model", Description: "🤖 Switch model"},
}

// handleCommand routes a /command to its handler. Unknown commands get a short
// hint. Each command operates on the sending chat's own session.
func (t *Transport) handleCommand(ctx context.Context, chatID int64, text string) {
	cmd := strings.TrimPrefix(strings.Fields(text)[0], "/")
	// Strip a possible @botname suffix (Telegram appends it in groups).
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	t.logger.Info("telegram", "command", "chat", chatID, "name", cmd)

	switch cmd {
	case "stop":
		t.cmdStop(ctx, chatID)
	case "compact":
		t.cmdCompact(ctx, chatID)
	case "reset":
		t.cmdReset(ctx, chatID)
	case "info":
		t.cmdInfo(ctx, chatID)
	case "context":
		t.cmdContext(ctx, chatID)
	case "thinking":
		t.cmdThinking(ctx, chatID)
	case "model":
		t.cmdModel(ctx, chatID)
	default:
		t.reply(ctx, chatID, "Unknown command. Try /stop, /compact, /reset, /info, /context, /thinking or /model.")
	}
}

// cmdStop interrupts any in-flight work on the chat's session.
func (t *Transport) cmdStop(ctx context.Context, chatID int64) {
	p := t.pump(chatID)
	if p == nil {
		t.reply(ctx, chatID, "Nothing is running.")
		return
	}
	if _, err := t.api.StopSession(p.sessionID); err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	t.reply(ctx, chatID, "Stopped.")
}

// cmdCompact triggers conversation compaction on the chat's session. The start
// message reflects whether it ran now or was queued behind current work; the
// completion (or failure) is reported by the SSE drain (compact_start/end).
func (t *Transport) cmdCompact(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	// Mark this compaction as user-requested BEFORE the call: the server runs
	// Compact() asynchronously and can emit compact_start before ExecCommand even
	// returns, so the flag must already be set when the drain sees that event.
	p.compactExpected.Store(true)
	_, err = t.api.ExecCommand(p.sessionID, "compact", nil)
	if err != nil {
		p.compactExpected.Store(false) // call failed — no compaction will happen
		// API errors render with details (pretty JSON); plain errors as ⚠️ message.
		t.replyError(ctx, chatID, err)
		return
	}
	t.reply(ctx, chatID, "🗜 Compacting the conversation…")
}

// cmdReset wipes the session's history and stats, returning it to a blank state
// while preserving identity (model, thinking, name).
func (t *Transport) cmdReset(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	if _, err := t.api.ExecCommand(p.sessionID, "reset", nil); err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	t.reply(ctx, chatID, "🔄 Session history and stats wiped. Starting fresh.")
}

// cmdInfo reports the same picture as the TUI footer: harness version + session
// name, the model with its context window/usage and thinking level, token/cache/
// cost usage, and the connected MCPs + schedules owned by THIS session (a
// schedule only fires in its owner session, so that's the honest count).
// Uses a single GET /api/sessions/{id}/info call instead of the four separate
// API calls the old implementation required.
func (t *Transport) cmdInfo(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	info, err := t.api.GetSessionInfo(p.sessionID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}

	sess := info.Session
	stats := sess.Stats

	// row pads the label to 10 chars. The entire data block is wrapped in a
	// Telegram code fence (monospace) so spacing aligns visually — Telegram's
	// default font is proportional, making space-based padding useless outside
	// a code block.
	row := func(label, value string) string {
		return fmt.Sprintf("%-10s%s\n", label, value)
	}

	// Build the monospace data block.
	var data strings.Builder

	// Identity
	data.WriteString(row("harness", info.Version))
	name := sess.Name
	if name == "" {
		name = sess.ID[:8]
	}
	data.WriteString(row("session", name))

	// Model + runtime config
	data.WriteByte('\n')
	data.WriteString(row("model", sess.Model))
	thinking := sess.Thinking
	if thinking == "" {
		thinking = "off"
	}
	data.WriteString(row("thinking", thinking))
	data.WriteString(row("iters", fmt.Sprintf("max %d", sess.MaxIterations)))
	if stats.ContextWindow > 0 {
		data.WriteString(row("context",
			fmt.Sprintf("%.1f%% of %s tokens", stats.ContextUsage*100, compactNum(int64(stats.ContextWindow)))))
	}

	// Token / cache / cost
	data.WriteByte('\n')
	data.WriteString(row("tokens",
		fmt.Sprintf("↑%s ↓%s", compactNum(int64(stats.InputTokens)), compactNum(int64(stats.OutputTokens)))))
	if stats.CacheRead > 0 || stats.CacheWrite > 0 {
		data.WriteString(row("cache",
			fmt.Sprintf("R%s W%s", compactNum(int64(stats.CacheRead)), compactNum(int64(stats.CacheWrite)))))
	}
	data.WriteString(row("cost", fmt.Sprintf("$%.4f", stats.CostUSD)))

	// Environment
	data.WriteByte('\n')
	data.WriteString(row("mcps", fmt.Sprintf("%d connected", info.MCPConnected)))
	data.WriteString(row("schedules", fmt.Sprintf("%d", info.ScheduleCount)))

	// Runtime state (busy / queued)
	if info.Busy {
		queued := ""
		if info.QueueDepth > 0 {
			queued = fmt.Sprintf(" (%d queued)", info.QueueDepth)
		}
		data.WriteString(fmt.Sprintf("\n⚙ busy%s\n", queued))
	}

	// Assemble: title (bold) + code block (monospace, aligned).
	var b strings.Builder
	b.WriteString("📊 *Session info*\n\n")
	b.WriteString("```\n")
	b.WriteString(strings.TrimRight(data.String(), "\n"))
	b.WriteString("\n```")

	t.reply(ctx, chatID, b.String())
}

// cmdContext reports the context window breakdown for the chat's session.
func (t *Transport) cmdContext(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	bd, err := t.api.GetSessionContext(p.sessionID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}

	win := bd.ContextWindow
	if win == 0 {
		t.reply(ctx, chatID, "⚠️ context window size unknown (no model metadata yet).")
		return
	}

	const gridCells = 64

	cn := func(n int) string { return compactNum(int64(n)) }

	cellSize := float64(win) / gridCells

	cellsFor := func(n int) int {
		if n <= 0 {
			return 0
		}
		c := int(math.Floor(float64(n) / cellSize))
		if c < 1 {
			c = 1
		}
		if c > gridCells {
			c = gridCells
		}
		return c
	}

	sC := cellsFor(bd.System)
	tC := cellsFor(bd.Tools)
	cC := cellsFor(bd.Conversation)
	fC := gridCells - sC - tC - cC
	if fC < 0 {
		fC = 0
	}

	seq := strings.Repeat("S", sC) + strings.Repeat("T", tC) +
		strings.Repeat("C", cC) + strings.Repeat(".", fC)
	for len(seq) < gridCells {
		seq += "."
	}
	seq = seq[:gridCells]

	legend := []string{
		fmt.Sprintf("S system  %6s", cn(bd.System)),
		fmt.Sprintf("T tools   %6s", cn(bd.Tools)),
		fmt.Sprintf("C conv    %6s", cn(bd.Conversation)),
		fmt.Sprintf(". free    %6s", cn(bd.FreeSpace)),
		"",
		fmt.Sprintf("cell ≈ %.1fk", float64(win)/gridCells/1000),
	}

	var data strings.Builder

	data.WriteString("+-+-+-+-+-+-+-+-+\n")
	for row := 0; row < 8; row++ {
		// Cell row
		data.WriteString("|")
		for col := 0; col < 8; col++ {
			data.WriteByte(seq[row*8+col])
			if col < 7 {
				data.WriteString("|")
			}
		}
		data.WriteString("|")
		if row < len(legend) {
			data.WriteString("   " + legend[row])
		}
		data.WriteByte('\n')
		if row < 7 {
			data.WriteString("+-+-+-+-+-+-+-+-+\n")
		}
	}
	data.WriteString("+-+-+-+-+-+-+-+-+\n")
	data.WriteString("\n" + strings.Repeat("-", 36) + "\n")

	if bd.LastRealTotal > 0 && win > 0 {
		usedPct := float64(bd.LastRealTotal) / float64(win) * 100
		uf := int(usedPct / 100 * 28)
		if uf < 1 {
			uf = 1
		}
		bigBar := strings.Repeat("#", uf) + strings.Repeat(".", 28-uf)
		data.WriteString(fmt.Sprintf("actual  [%s]\n", bigBar))
		data.WriteString(fmt.Sprintf("        %.1f%% used\n", usedPct))
		data.WriteString(fmt.Sprintf("        %s used · %s free · %s\n",
			cn(bd.LastRealTotal), cn(bd.FreeSpace), cn(win)))
	} else {
		data.WriteString(fmt.Sprintf("estimated  ~%s tokens\n", cn(bd.EstimatedTotal)))
		data.WriteString("(no turn yet)\n")
	}

	var b strings.Builder
	b.WriteString("📐 *Context breakdown*\n\n")
	b.WriteString("```\n")
	b.WriteString(strings.TrimRight(data.String(), "\n"))
	b.WriteString("\n```")

	t.reply(ctx, chatID, b.String())
}

// compactNum renders a token count compactly (1300 -> 1.3k, 406600 -> 406.6k,
// 200000 -> 200k), matching the TUI footer. Round values drop the ".0".
func compactNum(n int64) string {
	switch {
	case n >= 1_000_000:
		return trimDotZero(fmt.Sprintf("%.1f", float64(n)/1_000_000)) + "M"
	case n >= 1_000:
		return trimDotZero(fmt.Sprintf("%.1f", float64(n)/1_000)) + "k"
	default:
		return fmt.Sprintf("%d", n)
	}
}

// trimDotZero drops a trailing ".0" so 200.0 -> 200 while 1.3 stays 1.3.
func trimDotZero(s string) string {
	return strings.TrimSuffix(s, ".0")
}

// cmdModel sends an inline keyboard grouped by provider so the user can pick
// a model. Each button shows the short model name (after the provider/);
// callback_data carries the full "provider/model" string.
func (t *Transport) cmdModel(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}

	models, err := t.api.ListModels()
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	if len(models) == 0 {
		t.reply(ctx, chatID, "⚠️ No models available — connect a provider first.")
		return
	}

	// Get current model to mark it.
	current := ""
	if info, err := t.api.GetSessionInfo(p.sessionID); err == nil {
		current = info.Session.Model
	}

	// Group models by provider, preserving insertion order.
	type providerGroup struct {
		name   string
		models []client.Model
	}
	var order []string
	groups := map[string]*providerGroup{}
	for _, m := range models {
		if _, ok := groups[m.Provider]; !ok {
			order = append(order, m.Provider)
			groups[m.Provider] = &providerGroup{name: m.Provider}
		}
		groups[m.Provider].models = append(groups[m.Provider].models, m)
	}

	// Build keyboard: one header row (disabled label) + model rows of 2 per provider.
	var rows [][]InlineKeyboardButton
	for _, provName := range order {
		g := groups[provName]
		// Provider header — a non-clickable label row (callback_data = "noop").
		rows = append(rows, []InlineKeyboardButton{
			{Text: "── " + provName + " ──", CallbackData: "noop"},
		})
		// Model buttons in pairs of 2 per row.
		for i := 0; i < len(g.models); i += 2 {
			var row []InlineKeyboardButton
			for j := i; j < i+2 && j < len(g.models); j++ {
				m := g.models[j]
				// Short label: just the part after provider/
				short := m.Model
				if idx := strings.LastIndex(m.Model, "/"); idx >= 0 {
					short = m.Model[idx+1:]
				}
				if m.Model == current {
					short = "✓ " + short
				}
				row = append(row, InlineKeyboardButton{
					Text:         short,
					CallbackData: "model:" + m.Model,
				})
			}
			rows = append(rows, row)
		}
	}

	kb := InlineKeyboardMarkup{InlineKeyboard: rows}
	currentShort := current
	if idx := strings.LastIndex(current, "/"); idx >= 0 {
		currentShort = current[idx+1:]
	}

	msgID, err := t.bot.SendMessageWithKeyboard(ctx, chatID,
		fmt.Sprintf("🤖 *Model* — current: `%s`\nPick a model:", currentShort), kb)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}

	p.pendingKbMu.Lock()
	p.pendingKb = &pendingKeyboard{messageID: msgID, command: "model"}
	p.pendingKbMu.Unlock()
}

// thinkingLevels are the valid thinking levels in display order.
var thinkingLevels = []string{"off", "low", "medium", "high", "xhigh"}

// cmdThinking sends an inline keyboard so the user can pick a thinking level.
func (t *Transport) cmdThinking(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}

	// Get current level to show it in the prompt.
	current := ""
	if info, err := t.api.GetSessionInfo(p.sessionID); err == nil {
		current = info.Session.Thinking
		if current == "" {
			current = "off"
		}
	}

	// Build one row of buttons — one per level, current one marked with ✓.
	var row []InlineKeyboardButton
	for _, level := range thinkingLevels {
		label := level
		if level == current {
			label = "✓ " + level
		}
		row = append(row, InlineKeyboardButton{
			Text:         label,
			CallbackData: "thinking:" + level,
		})
	}
	kb := InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{row}}

	msgID, err := t.bot.SendMessageWithKeyboard(ctx, chatID,
		fmt.Sprintf("🧠 *Thinking level* — current: `%s`\nPick a new level:", current), kb)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}

	// Record pending keyboard so the callback handler knows what to do.
	p.pendingKbMu.Lock()
	p.pendingKb = &pendingKeyboard{messageID: msgID, command: "thinking"}
	p.pendingKbMu.Unlock()
}
