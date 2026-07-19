// Package telegram is a Harness transport that exposes the agent through a
// Telegram bot. Like the TUI, it runs an in-process server and drives it over
// HTTP/SSE; the "display" is Telegram instead of a terminal. Incoming chat
// messages become prompts, and the agent's text replies become outgoing
// messages — one harness session per chat.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Bot is a minimal Telegram Bot API client — just the two endpoints this
// transport needs (getUpdates for long polling, sendMessage to reply), built on
// the standard library so no external dependency is pulled in.
type Bot struct {
	token string
	http  *http.Client
	api   string
}

// NewBot builds a client for the given bot token.
func NewBot(token string) *Bot {
	return &Bot{
		token: token,
		// No overall timeout: getUpdates long-polls. Per-call deadlines come from
		// the request context.
		http: &http.Client{},
		api:  "https://api.telegram.org/bot" + token,
	}
}

// ── Bot API types (only the fields we use) ────────────────────────────────

// Update is one incoming event from getUpdates.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is a chat message.
type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// User is the sender of a message.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is where a message lives (private chat, group, …). ID is stable per chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// apiResponse is the envelope Telegram wraps every result in.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// ── Methods ───────────────────────────────────────────────────────────────

// GetMe verifies the token and returns the bot's own user record. Used at
// startup to fail fast on a bad token.
func (b *Bot) GetMe(ctx context.Context) (*User, error) {
	raw, err := b.call(ctx, "getMe", nil)
	if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUpdates long-polls for new updates starting at offset. It blocks up to
// timeout seconds server-side; the request context bounds the whole call.
func (b *Bot) GetUpdates(ctx context.Context, offset, timeout int) ([]Update, error) {
	body := map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message"},
	}
	raw, err := b.call(ctx, "getUpdates", body)
	if err != nil {
		return nil, err
	}
	var updates []Update
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// SendMessage posts text to a chat. parseMode may be "MarkdownV2", "Markdown",
// "HTML", or "" for plain text. Returns the Telegram error code (e.g. 400 for a
// markdown-parse failure) so the caller can retry as plain text.
func (b *Bot) SendMessage(ctx context.Context, chatID int64, text, parseMode string) error {
	body := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		body["parse_mode"] = parseMode
	}
	_, err := b.call(ctx, "sendMessage", body)
	return err
}

// SendChatAction shows a transient status in the chat (e.g. "typing"). Best
// effort — errors are ignored by callers.
func (b *Bot) SendChatAction(ctx context.Context, chatID int64, action string) error {
	_, err := b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action})
	return err
}

// ── transport ─────────────────────────────────────────────────────────────

// apiError carries Telegram's error code so callers can branch (e.g. 400 →
// retry without markdown).
type apiError struct {
	Code int
	Desc string
}

func (e *apiError) Error() string { return fmt.Sprintf("telegram %d: %s", e.Code, e.Desc) }

// call POSTs a JSON body to a Bot API method and returns the raw result.
func (b *Bot) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.api+"/"+method, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var env apiResponse
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("telegram: decode response: %w", err)
	}
	if !env.OK {
		return nil, &apiError{Code: env.ErrorCode, Desc: env.Description}
	}
	return env.Result, nil
}

// errorCode extracts Telegram's error code from an error, or 0 if it isn't an
// apiError.
func errorCode(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

// backoff is the pause after a failed getUpdates poll before retrying.
const backoff = 3 * time.Second
