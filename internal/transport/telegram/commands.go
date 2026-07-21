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
	{Command: "new", Description: "Start a fresh session"},
	{Command: "stop", Description: "Stop the current work"},
	{Command: "compact", Description: "Summarize & compact the conversation"},
	{Command: "info", Description: "Session & model info"},
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

// cmdCompact triggers conversation compaction on the chat's session.
func (t *Transport) cmdCompact(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	if err := t.api.ExecCommand(p.sessionID, "compact", nil); err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	t.reply(ctx, chatID, "Compacting the conversation…")
}

// cmdInfo reports harness version, the session's model/thinking, and usage stats.
func (t *Transport) cmdInfo(ctx context.Context, chatID int64) {
	p, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	var b strings.Builder
	b.WriteString("*Session info*\n")

	if info, err := t.api.GetServerInfo(); err == nil {
		if v, _ := info["version"].(string); v != "" {
			fmt.Fprintf(&b, "harness: %s\n", v)
		}
	}
	if meta, err := t.api.GetSession(p.sessionID); err == nil {
		if m, _ := meta["model"].(string); m != "" {
			fmt.Fprintf(&b, "model: %s\n", m)
		}
		if th, _ := meta["thinking"].(string); th != "" {
			fmt.Fprintf(&b, "thinking: %s\n", th)
		}
		if stats, ok := meta["stats"].(map[string]any); ok {
			in := numField(stats, "input_tokens")
			out := numField(stats, "output_tokens")
			fmt.Fprintf(&b, "tokens: %d in / %d out\n", in, out)
			if c, ok := stats["cost_usd"].(float64); ok {
				fmt.Fprintf(&b, "cost: $%.4f\n", c)
			}
		}
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
