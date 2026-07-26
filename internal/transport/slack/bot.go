// Package slack is a Harness transport that exposes the agent through Slack
// using browser session tokens (xoxc + xoxd). Like the Telegram transport, it
// runs an in-process server and drives it over HTTP/SSE; the "display" is Slack
// instead of a terminal. Incoming DMs and channel mentions become prompts, and
// the agent's text replies become outgoing Slack messages — one harness session
// per channel.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

const (
	backoff = 5 * time.Second
)

// Bot is a minimal Slack client for the xoxc/xoxd browser-session auth flow.
// It covers the three operations this transport needs:
//   - AuthTest  — verify tokens and get our own user ID
//   - RTMConnect — get the WebSocket URL
//   - PostMessage — send a reply
type Bot struct {
	workspace string // e.g. "https://myco.slack.com"
	xoxc      string // xoxc-... API token (Authorization header)
	xoxd      string // xoxd-... session cookie (Cookie: d=...)
	http      *http.Client
}

// NewBot builds a Slack client for the given workspace and browser session tokens.
func NewBot(workspace, xoxc, xoxd string) *Bot {
	return &Bot{
		workspace: workspace,
		xoxc:      xoxc,
		xoxd:      xoxd,
		http:      &http.Client{},
	}
}

// ── Slack API types (only what we use) ────────────────────────────────────

// AuthTestResponse is the response from auth.test.
type AuthTestResponse struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	UserID string `json:"user_id"`
	Team   string `json:"team"`
	URL    string `json:"url"`
}

// rtmConnectResponse is the response from rtm.connect.
type rtmConnectResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	URL   string `json:"url"` // wss:// WebSocket URL
}

// postMessageResponse is the response from chat.postMessage.
type postMessageResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// RTMEvent is one event received from the RTM WebSocket.
type RTMEvent struct {
	Type    string      `json:"type"`
	Channel string      `json:"channel"`  // channel or DM id
	User    string      `json:"user"`     // sender user id
	Text    string      `json:"text"`     // message text
	TS      string      `json:"ts"`       // message timestamp (Slack unique id)
	BotID   string      `json:"bot_id"`   // non-empty if sent by a bot
	SubType string      `json:"subtype"`  // "message_changed", "bot_message", etc.
	Files   []SlackFile `json:"files"`    // file attachments (images, text files, etc.)
}

// ── API calls ─────────────────────────────────────────────────────────────

// AuthTest verifies the tokens and returns our own user ID.
func (b *Bot) AuthTest(ctx context.Context) (*AuthTestResponse, error) {
	data, err := b.apiCall(ctx, "auth.test", nil)
	if err != nil {
		return nil, err
	}
	var r AuthTestResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("slack auth.test: decode: %w", err)
	}
	if !r.OK {
		return nil, fmt.Errorf("slack auth.test: %s", r.Error)
	}
	return &r, nil
}

// RTMConnect calls rtm.connect and returns the WebSocket URL to dial.
func (b *Bot) RTMConnect(ctx context.Context) (string, error) {
	data, err := b.apiCall(ctx, "rtm.connect", nil)
	if err != nil {
		return "", err
	}
	var r rtmConnectResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("slack rtm.connect: decode: %w", err)
	}
	if !r.OK {
		return "", fmt.Errorf("slack rtm.connect: %s", r.Error)
	}
	return r.URL, nil
}

// PostMessage sends text to a Slack channel or DM.
func (b *Bot) PostMessage(ctx context.Context, channelID, text string) error {
	params := map[string]string{
		"channel": channelID,
		"text":    text,
	}
	data, err := b.apiCall(ctx, "chat.postMessage", params)
	if err != nil {
		return err
	}
	var r postMessageResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("slack chat.postMessage: decode: %w", err)
	}
	if !r.OK {
		return fmt.Errorf("slack chat.postMessage: %s", r.Error)
	}
	return nil
}

// DialRTM dials the RTM WebSocket with the required xoxc/xoxd auth headers.
// Returns the connection for the caller to read events from.
func (b *Bot) DialRTM(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+b.xoxc)
	headers.Set("Cookie", "d="+b.xoxd)
	headers.Set("Origin", "https://api.slack.com")
	headers.Set("User-Agent", "Mozilla/5.0 (compatible; harness-slack/1.0)")

	dialer := websocket.Dialer{HandshakeTimeout: 30 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, fmt.Errorf("slack RTM dial: %w", err)
	}
	return conn, nil
}

// ── Internal HTTP helper ──────────────────────────────────────────────────

// apiCall makes a POST to workspace/api/<method> with xoxc/xoxd auth and
// optional form params. Returns the raw JSON body.
func (b *Bot) apiCall(ctx context.Context, method string, params map[string]string) ([]byte, error) {
	form := url.Values{}
	form.Set("token", b.xoxc)
	for k, v := range params {
		form.Set(k, v)
	}

	apiURL := b.workspace + "/api/" + method
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL,
		bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("slack %s: build request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+b.xoxc)
	req.Header.Set("Cookie", "d="+b.xoxd)
	req.Header.Set("Origin", "https://app.slack.com")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; harness-slack/1.0)")

	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack %s: request: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("slack %s: read: %w", method, err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("slack %s: HTTP %d", method, resp.StatusCode)
	}
	return body, nil
}
