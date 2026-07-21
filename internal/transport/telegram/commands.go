package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/internal/logx"
)

// botCommands is the command menu registered with Telegram (setMyCommands) and
// the set this transport handles. Descriptions show in the "/" suggestion list.
var botCommands = []BotCommand{
	{Command: "new", Description: "🆕 Start a fresh session"},
	{Command: "stop", Description: "🛑 Stop the current work"},
	{Command: "compact", Description: "🗜 Summarize & compact the conversation"},
	{Command: "info", Description: "📊 Session & model info"},
}

// handleCommand routes a /command to its handler. Unknown commands get a short
// hint. Each command operates on the sending chat's own session.
func (t *Transport) handleCommand(ctx context.Context, chatID int64, text string) {
	cmd := strings.TrimPrefix(strings.Fields(text)[0], "/")
	// Strip a possible @botname suffix (Telegram appends it in groups).
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	logx.Info("telegram", "command", "chat", chatID, "name", cmd)

	switch cmd {
	case "new":
		t.cmdNew(ctx, chatID)
	case "stop":
		t.cmdStop(ctx, chatID)
	case "compact":
		t.cmdCompact(ctx, chatID)
	case "info":
		t.cmdInfo(ctx, chatID)
	default:
		t.reply(ctx, chatID, "Unknown command. Try /new, /stop, /compact or /info.")
	}
}

// cmdNew closes the chat's current session and starts a blank one.
func (t *Transport) cmdNew(ctx context.Context, chatID int64) {
	t.resetChat(ctx, chatID)
	if _, err := t.pumpFor(ctx, chatID); err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	t.reply(ctx, chatID, "Started a fresh session.")
}

// cmdStop interrupts any in-flight work on the chat's session.
func (t *Transport) cmdStop(ctx context.Context, chatID int64) {
	p := t.pump(chatID)
	if p == nil {
		t.reply(ctx, chatID, "Nothing is running.")
		return
	}
	if err := t.api.StopSession(p.sessionID); err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
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
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	status, err := t.api.ExecCommand(p.sessionID, "compact", nil)
	if err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	// Mark this compaction as user-requested so the drain's compact_start doesn't
	// re-announce it as automatic.
	p.compactExpected.Store(true)
	if status == "queued" {
		t.reply(ctx, chatID, "🗜 Compaction queued — it'll run after the current task.")
	} else {
		t.reply(ctx, chatID, "🗜 Compacting the conversation…")
	}
}

// cmdInfo reports the same picture as the TUI footer: harness version + session
// name, the model with its context window/usage and thinking level, token/cache/
// cost usage, and the connected MCPs + schedules owned by THIS session (a
// schedule only fires in its owner session, so that's the honest count).
func (t *Transport) cmdInfo(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	var b strings.Builder
	b.WriteString("📊 *Session info*\n\n")

	if info, err := t.api.GetServerInfo(); err == nil {
		if v, _ := info["version"].(string); v != "" {
			fmt.Fprintf(&b, "harness %s\n", v)
		}
	}
	if meta, err := t.api.GetSession(p.sessionID); err == nil {
		if name, _ := meta["name"].(string); name != "" {
			fmt.Fprintf(&b, "Session: %s\n", name)
		}
		b.WriteByte('\n')
		if m, _ := meta["model"].(string); m != "" {
			fmt.Fprintf(&b, "Model: %s\n", m)
		}
		if stats, ok := meta["stats"].(map[string]any); ok {
			win := numField(stats, "context_window")
			pct, _ := stats["context_usage"].(float64)
			if win > 0 {
				fmt.Fprintf(&b, "Context: %s window · %.1f%% used\n", compactNum(win), pct*100)
			}
		}
		if th, _ := meta["thinking"].(string); th != "" {
			fmt.Fprintf(&b, "Thinking: %s\n", th)
		}
		if stats, ok := meta["stats"].(map[string]any); ok {
			b.WriteByte('\n')
			fmt.Fprintf(&b, "Tokens: ↑%s ↓%s\n",
				compactNum(numField(stats, "input_tokens")), compactNum(numField(stats, "output_tokens")))
			cr, cw := numField(stats, "cache_read"), numField(stats, "cache_write")
			if cr > 0 || cw > 0 {
				fmt.Fprintf(&b, "Cache: R%s W%s\n", compactNum(cr), compactNum(cw))
			}
			if c, ok := stats["cost_usd"].(float64); ok {
				fmt.Fprintf(&b, "Cost: $%.3f\n", c)
			}
		}
	}

	b.WriteByte('\n')
	fmt.Fprintf(&b, "MCPs: %d connected\n", t.api.CountConnectedMCPs())
	// Schedules only fire when this bot runs the engine (--scheduler); without it,
	// reporting a count would be misleading (they'd never run).
	if t.opts.Scheduler {
		fmt.Fprintf(&b, "Schedules: %d\n", t.api.CountSchedules(p.sessionID))
	}

	t.reply(ctx, chatID, strings.TrimRight(b.String(), "\n"))
}

// numField reads an integer-ish JSON number from a map (JSON numbers decode as
// float64).
func numField(m map[string]any, key string) int64 {
	if f, ok := m[key].(float64); ok {
		return int64(f)
	}
	return 0
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
