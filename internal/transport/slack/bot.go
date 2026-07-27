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
	"os"
	"path/filepath"
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
	Channel string      `json:"channel"` // channel or DM id
	User    string      `json:"user"`    // sender user id
	Text    string      `json:"text"`    // message text
	TS      string      `json:"ts"`      // message timestamp (Slack unique id)
	BotID   string      `json:"bot_id"`  // non-empty if sent by a bot
	SubType string      `json:"subtype"` // "message_changed", "bot_message", etc.
	Files   []SlackFile `json:"files"`   // file attachments (images, text files, etc.)
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

// ── Channels, users, DM opening ──────────────────────────────────────────

// SlackChannel is one channel entry from conversations.list.
type SlackChannel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsPrivate  bool   `json:"is_private"`
	NumMembers int    `json:"num_members"`
}

// SlackUser is one user entry from users.list.
type SlackUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"` // @handle
	Deleted bool   `json:"deleted"`
	IsBot   bool   `json:"is_bot"`
	Profile struct {
		RealName    string `json:"real_name"`
		DisplayName string `json:"display_name"`
	} `json:"profile"`
}

// ListChannels returns all non-archived public+private channels the user can see.
// Paginates automatically up to limit total channels (0 = 1000 max).
func (b *Bot) ListChannels(ctx context.Context, limit int) ([]SlackChannel, error) {
	if limit <= 0 {
		limit = 1000
	}
	var all []SlackChannel
	cursor := ""
	for {
		params := map[string]string{
			"exclude_archived": "true",
			"types":            "public_channel,private_channel",
			"limit":            "200",
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := b.apiCall(ctx, "conversations.list", params)
		if err != nil {
			return nil, fmt.Errorf("slack conversations.list: %w", err)
		}
		var resp struct {
			OK               bool           `json:"ok"`
			Error            string         `json:"error,omitempty"`
			Channels         []SlackChannel `json:"channels"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("slack conversations.list: decode: %w", err)
		}
		if !resp.OK {
			return nil, fmt.Errorf("slack conversations.list: %s", resp.Error)
		}
		all = append(all, resp.Channels...)
		if resp.ResponseMetadata.NextCursor == "" || len(all) >= limit {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// ListUsers returns all non-bot, non-deleted users in the workspace.
// Paginates automatically up to limit total users (0 = 1000 max).
func (b *Bot) ListUsers(ctx context.Context, limit int) ([]SlackUser, error) {
	if limit <= 0 {
		limit = 1000
	}
	var all []SlackUser
	cursor := ""
	for {
		params := map[string]string{"limit": "200"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := b.apiCall(ctx, "users.list", params)
		if err != nil {
			return nil, fmt.Errorf("slack users.list: %w", err)
		}
		var resp struct {
			OK               bool        `json:"ok"`
			Error            string      `json:"error,omitempty"`
			Members          []SlackUser `json:"members"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("slack users.list: decode: %w", err)
		}
		if !resp.OK {
			return nil, fmt.Errorf("slack users.list: %s", resp.Error)
		}
		for _, u := range resp.Members {
			if !u.Deleted && !u.IsBot {
				all = append(all, u)
			}
		}
		if resp.ResponseMetadata.NextCursor == "" || len(all) >= limit {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// OpenDM opens a direct message channel with a user and returns the DM channel
// ID. Required before posting to a user ID — Slack DMs are channels with D…
// IDs obtained via conversations.open.
func (b *Bot) OpenDM(ctx context.Context, userID string) (string, error) {
	raw, err := b.apiCall(ctx, "conversations.open", map[string]string{"users": userID})
	if err != nil {
		return "", fmt.Errorf("slack conversations.open: %w", err)
	}
	var resp struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error,omitempty"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("slack conversations.open: decode: %w", err)
	}
	if !resp.OK {
		return "", fmt.Errorf("slack conversations.open: %s", resp.Error)
	}
	return resp.Channel.ID, nil
}

// ── File upload (3-step API) ──────────────────────────────────────────────

// uploadURLResponse is the response from files.getUploadURLExternal.
type uploadURLResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	UploadURL string `json:"upload_url"`
	FileID    string `json:"file_id"`
}

// completeUploadResponse is the response from files.completeUploadExternal.
type completeUploadResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// UploadFile uploads a local file to Slack and shares it in the given channel
// using the current 3-step API (files.upload was deprecated Nov 2025):
//
//  1. files.getUploadURLExternal — get a pre-signed upload URL + file ID
//  2. PUT file bytes to the upload URL (no auth headers needed)
//  3. files.completeUploadExternal — finalize and share in channel
//
// initialComment is the text that accompanies the file in the channel
// (stripped of the <slack:uploadFile> tags by the caller).
func (b *Bot) UploadFile(ctx context.Context, channelID, filePath, initialComment string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("slack upload: read file: %w", err)
	}
	filename := filepath.Base(filePath)

	// Step 1 — get upload URL.
	raw, err := b.apiCall(ctx, "files.getUploadURLExternal", map[string]string{
		"filename": filename,
		"length":   fmt.Sprintf("%d", len(data)),
	})
	if err != nil {
		return fmt.Errorf("slack upload: getUploadURLExternal: %w", err)
	}
	var urlResp uploadURLResponse
	if err := json.Unmarshal(raw, &urlResp); err != nil {
		return fmt.Errorf("slack upload: decode url response: %w", err)
	}
	if !urlResp.OK {
		return fmt.Errorf("slack upload: getUploadURLExternal: %s", urlResp.Error)
	}

	// Step 2 — PUT file bytes to the pre-signed URL (no auth required).
	req, err := http.NewRequestWithContext(ctx, "POST", urlResp.UploadURL,
		bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("slack upload: build PUT: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack upload: PUT file: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack upload: PUT returned %d", resp.StatusCode)
	}

	// Step 3 — complete upload and share in channel.
	filesJSON := fmt.Sprintf(`[{"id":%q,"title":%q}]`, urlResp.FileID, filename)
	completeParams := map[string]string{
		"files":           filesJSON,
		"channel_id":      channelID,
		"initial_comment": initialComment,
	}
	raw, err = b.apiCall(ctx, "files.completeUploadExternal", completeParams)
	if err != nil {
		return fmt.Errorf("slack upload: completeUploadExternal: %w", err)
	}
	var completeResp completeUploadResponse
	if err := json.Unmarshal(raw, &completeResp); err != nil {
		return fmt.Errorf("slack upload: decode complete response: %w", err)
	}
	if !completeResp.OK {
		return fmt.Errorf("slack upload: completeUploadExternal: %s", completeResp.Error)
	}
	return nil
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
