package telegram

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/server"
)

// Options configures the Telegram transport.
type Options struct {
	Token       string // bot token (required)
	Model       string // model override; empty = server default
	Thinking    string // thinking level override; empty = server default
	Scheduler   bool   // run the cron engine (schedules fire into their owner chat)
	AllowUnpair bool   // auto-pair: accept any chat, adding it to the allowlist on first contact
}

// Transport is the running Telegram bot: it owns the agent, the in-process
// server, the bot API client, and one live SSE pump per active chat.
type Transport struct {
	opts  Options
	agent *agent.Agent
	api   *apiClient
	bot   *Bot
	store *store
	model string
	cwd   string

	mu    sync.Mutex
	pumps map[int64]*chatPump // chat id → its live session pump

	pendingAlbums *albums // in-flight photo albums, keyed by media_group_id
}

// Run starts the bot and blocks until ctx is cancelled. It builds the agent,
// launches the internal server, verifies the token, then long-polls for
// messages — each becoming a prompt for that chat's session.
func Run(ctx context.Context, a *agent.Agent, opts Options) error {
	if opts.Token == "" {
		return fmt.Errorf("telegram: a bot token is required (--token or TELEGRAM_BOT_TOKEN)")
	}

	st, err := openStore("")
	if err != nil {
		return err
	}

	// In-process server — the transport talks to it over HTTP/SSE, exactly like
	// the TUI, keeping the frontend/backend split clean.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("telegram: bind server: %w", err)
	}
	srv := server.NewServer(a, server.ServerOptions{Verbose: false})
	go srv.Serve(listener) //nolint:errcheck

	cwd, _ := os.Getwd()
	t := &Transport{
		opts:  opts,
		agent: a,
		api:   newAPIClient(listener.Addr().String()),
		bot:   NewBot(opts.Token),
		store:         st,
		cwd:           cwd,
		pumps:         make(map[int64]*chatPump),
		pendingAlbums: newAlbums(),
	}

	// Resolve the model once (shared by all chats).
	if err := t.resolveModel(); err != nil {
		return err
	}

	// Verify the token before entering the loop.
	me, err := t.bot.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram: invalid token or unreachable API: %w", err)
	}
	log.Printf("telegram: connected as @%s — model=%s scheduler=%v paired=%d allow-unpair=%v",
		me.Username, t.model, opts.Scheduler, len(st.allowlist()), opts.AllowUnpair)
	if len(st.allowlist()) == 0 && !opts.AllowUnpair {
		log.Printf("telegram: WARN no paired chats — everyone is rejected until you run 'harness telegram pair <chat_id>' (or use --allow-unpair)")
	}

	return t.pollLoop(ctx)
}

// resolveModel picks the model: the --model override if active, else the
// persisted default, else the first active model.
func (t *Transport) resolveModel() error {
	models, err := t.api.ListModels()
	if err != nil {
		return fmt.Errorf("telegram: reach server: %w", err)
	}
	if len(models) == 0 {
		return fmt.Errorf("telegram: no active providers — connect one first (harness connect ...)")
	}
	active := map[string]bool{}
	first := ""
	for _, m := range models {
		if id, _ := m["model"].(string); id != "" {
			active[id] = true
			if first == "" {
				first = id
			}
		}
	}
	if t.opts.Model != "" && active[t.opts.Model] {
		t.model = t.opts.Model
		return nil
	}
	if s, err := t.api.GetSettings(); err == nil {
		if def, _ := s["active_model"].(string); def != "" && active[def] {
			t.model = def
			return nil
		}
	}
	t.model = first
	return nil
}

// pollLoop long-polls getUpdates and dispatches each message. It advances the
// offset past processed updates so none repeat.
func (t *Transport) pollLoop(ctx context.Context) error {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := t.bot.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("telegram: getUpdates: %v (retrying)", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			// Skip updates with nothing we handle (no text and no photo).
			if u.Message == nil || (u.Message.Text == "" && len(u.Message.Photo) == 0) {
				continue
			}
			t.handleMessage(ctx, u.Message)
		}
	}
}

// handleMessage turns one incoming chat message into a prompt (or a command).
func (t *Transport) handleMessage(ctx context.Context, msg *Message) {
	chatID := msg.Chat.ID
	if !t.authorize(ctx, chatID) {
		return
	}
	// Photos (single or album) — downloaded and sent as image prompts.
	if len(msg.Photo) > 0 {
		t.handlePhotoMessage(ctx, msg)
		return
	}

	text := strings.TrimSpace(msg.Text)

	// Commands.
	if strings.HasPrefix(text, "/") {
		t.handleCommand(ctx, chatID, text)
		return
	}

	pump, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	log.Printf("telegram: ← prompt from chat %d (%s): %s", chatID, sessionShort(pump.sessionID), oneLine(text, 200))
	// The typing indicator is driven by the SSE drain (turn_start→turn_end) so it
	// stays alive for the whole turn, not just Telegram's ~5s window.
	if err := t.api.SendPrompt(pump.sessionID, text); err != nil {
		t.reply(ctx, chatID, "⚠️ "+err.Error())
	}
}

// authorize reports whether a chat may use the bot. With --allow-unpair, an
// unknown chat is auto-paired (added to the allowlist) on first contact.
// Otherwise an un-paired chat is rejected: it's told how to pair, and the
// rejection is logged as a warning.
func (t *Transport) authorize(ctx context.Context, chatID int64) bool {
	if t.store.allowed(chatID) {
		return true
	}
	if t.opts.AllowUnpair {
		if added, err := t.store.pair(chatID); err == nil && added {
			log.Printf("telegram: auto-paired chat %d (--allow-unpair)", chatID)
		}
		return true
	}
	log.Printf("telegram: WARN rejected chat %d (not paired)", chatID)
	t.reply(ctx, chatID, fmt.Sprintf(
		"You're not authorized to use this bot yet.\n\nTo pair this chat, run on the host:\n`harness telegram pair %d`",
		chatID))
	return false
}

// handleCommand handles the small set of slash commands.
func (t *Transport) handleCommand(ctx context.Context, chatID int64, text string) {
	cmd := strings.Fields(text)[0]
	switch cmd {
	case "/start":
		if _, err := t.pumpFor(ctx, chatID); err != nil {
			t.reply(ctx, chatID, "⚠️ "+err.Error())
			return
		}
		t.reply(ctx, chatID, "Ready. Send a message and I'll get to work.")
	case "/new":
		t.resetChat(ctx, chatID)
		if _, err := t.pumpFor(ctx, chatID); err != nil {
			t.reply(ctx, chatID, "⚠️ "+err.Error())
			return
		}
		t.reply(ctx, chatID, "Started a fresh session.")
	default:
		t.reply(ctx, chatID, "Unknown command. Available: /start, /new")
	}
}

// oneLine collapses text to a single line and truncates it to max runes, for
// tidy one-line log entries.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// sessionShort returns a short prefix of a session id for readable logs.
func sessionShort(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
