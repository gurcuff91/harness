// Package telegram is an input channel (agent.Channel) over a Telegram bot: a
// session listens to the bot, the messages of its trusted users become the
// session's prompts, and the replies of those turns go back to the chat they
// came from. See docs/plans/2026-11-22-telegram-channel-design.md.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// apiBase is the Bot API root (a variable so tests can point it at a fake).
var apiBase = "https://api.telegram.org"

// Bot is a minimal Telegram Bot API client on the standard library.
type Bot struct {
	token string
	http  *http.Client
}

// NewBot builds a client for the given bot token.
func NewBot(token string) *Bot {
	// No overall timeout: getUpdates long-polls; calls bound themselves with ctx.
	return &Bot{token: token, http: &http.Client{}}
}

func (b *Bot) methodURL(method string) string { return apiBase + "/bot" + b.token + "/" + method }

// ── Bot API types (only the fields used) ─────────────────────────────────

// Update is one incoming event from getUpdates.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is a chat message.
type Message struct {
	MessageID    int         `json:"message_id"`
	From         *User       `json:"from"`
	Chat         Chat        `json:"chat"`
	Text         string      `json:"text"`
	Caption      string      `json:"caption"`
	Photo        []PhotoSize `json:"photo"`
	MediaGroupID string      `json:"media_group_id"`
	Document     *Document   `json:"document"`
}

// Document is a file sent as a document.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// PhotoSize is one size of a photo; the largest is the best quality.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// User is a Telegram account (a person or the bot itself).
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is where a message lives (private chat, group, …).
type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// APIError is an error answered by the Bot API, with its code.
type APIError struct {
	Code int
	Desc string
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram %d: %s", e.Code, e.Desc) }

func errorCode(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

// ── Methods ──────────────────────────────────────────────────────────────

// GetMe verifies the token and returns the bot's own account.
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

// GetUpdates long-polls for messages from offset, up to timeout seconds.
func (b *Bot) GetUpdates(ctx context.Context, offset, timeout int) ([]Update, error) {
	raw, err := b.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeout, "allowed_updates": []string{"message"},
	})
	if err != nil {
		return nil, err
	}
	var ups []Update
	if err := json.Unmarshal(raw, &ups); err != nil {
		return nil, err
	}
	return ups, nil
}

// SendMessage posts text; parseMode "MarkdownV2" or "" (plain).
func (b *Bot) SendMessage(ctx context.Context, chatID int64, text, parseMode string) error {
	body := map[string]any{"chat_id": chatID, "text": text}
	if parseMode != "" {
		body["parse_mode"] = parseMode
	}
	_, err := b.call(ctx, "sendMessage", body)
	return err
}

// SendChatAction shows a transient status ("typing"); best effort.
func (b *Bot) SendChatAction(ctx context.Context, chatID int64, action string) error {
	_, err := b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action})
	return err
}

// BotCommand is one entry of the bot's command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands sets the bot's command menu (what "/" suggests).
func (b *Bot) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	_, err := b.call(ctx, "setMyCommands", map[string]any{"commands": cmds})
	return err
}

// Download fetches a file by file_id, refusing one over max bytes.
func (b *Bot) Download(ctx context.Context, fileID string, max int64) ([]byte, error) {
	raw, err := b.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return nil, err
	}
	var f struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.FilePath == "" {
		return nil, errors.New("telegram: getFile returned no file_path")
	}
	if f.FileSize > max {
		return nil, errTooLarge
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/file/bot"+b.token+"/"+f.FilePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, b.redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram: download: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errTooLarge
	}
	return data, nil
}

// SendFile uploads a local file with method ("sendPhoto", "sendAnimation",
// "sendDocument") in field ("photo", "animation", "document").
func (b *Bot) SendFile(ctx context.Context, method, field string, chatID int64, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var werr error
		defer func() { _ = pw.CloseWithError(werr) }()
		if werr = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10)); werr != nil {
			return
		}
		part, werr := mw.CreateFormFile(field, filepath.Base(path))
		if werr != nil {
			return
		}
		if _, werr = io.Copy(part, f); werr != nil {
			return
		}
		werr = mw.Close()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.methodURL(method), pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.http.Do(req)
	if err != nil {
		return b.redact(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var env apiResponse
	if json.Unmarshal(data, &env) == nil && !env.OK {
		return &APIError{Code: env.ErrorCode, Desc: env.Description}
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram %s: status %d", method, resp.StatusCode)
	}
	return nil
}

// redact strips the token from a transport error: every Bot API URL embeds it,
// and *url.Error prints the URL.
func (b *Bot) redact(err error) error {
	if err == nil || b.token == "" {
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = strings.ReplaceAll(ue.URL, b.token, "<redacted>")
		return err
	}
	if strings.Contains(err.Error(), b.token) {
		return errors.New(strings.ReplaceAll(err.Error(), b.token, "<redacted>"))
	}
	return err
}

func (b *Bot) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.methodURL(method), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, b.redact(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var env apiResponse
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("telegram %s: decode response: %w", method, err)
	}
	if !env.OK {
		return nil, &APIError{Code: env.ErrorCode, Desc: env.Description}
	}
	return env.Result, nil
}
