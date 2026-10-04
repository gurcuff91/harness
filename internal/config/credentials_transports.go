package config

import (
	"encoding/json"
	"fmt"
)

// ── Transport secrets ────────────────────────────────────────────────────
//
// Secrets the chat transports need, kept out of the settings store so they
// can live in a different (secret-grade) backend than ordinary state.
//
// Store layout:
//
//	telegram / bot_token → the bot token (JSON string)
//	slack    / session   → SlackSession

const (
	nsTelegramCreds = "telegram"
	keyTelegramBot  = "bot_token"
	nsSlackCreds    = "slack"
	keySlackSession = "session"
)

// SlackSession is the secret half of a Slack login: the browser session
// token (xoxc) and cookie (xoxd).
type SlackSession struct {
	XoxC string `json:"xoxc"`
	XoxD string `json:"xoxd"`
}

// TelegramToken returns the saved Telegram bot token, or "" if none.
func (m *CredentialsManager) TelegramToken() string {
	var token string
	getJSON(m.store, nsTelegramCreds, keyTelegramBot, &token)
	return token
}

// SetTelegramToken saves the Telegram bot token.
func (m *CredentialsManager) SetTelegramToken(token string) error {
	if token == "" {
		return fmt.Errorf("%w: telegram bot token is empty", ErrInvalidCredential)
	}
	return m.setJSON(nsTelegramCreds, keyTelegramBot, token)
}

// SlackSession returns the saved Slack session pair.
func (m *CredentialsManager) SlackSession() (SlackSession, bool) {
	var s SlackSession
	ok := getJSON(m.store, nsSlackCreds, keySlackSession, &s)
	return s, ok && s.XoxC != "" && s.XoxD != ""
}

// SetSlackSession saves the Slack session pair.
func (m *CredentialsManager) SetSlackSession(s SlackSession) error {
	if s.XoxC == "" || s.XoxD == "" {
		return fmt.Errorf("%w: slack session requires xoxc and xoxd", ErrInvalidCredential)
	}
	return m.setJSON(nsSlackCreds, keySlackSession, s)
}

// setJSON encodes v and stores it under (namespace, key).
func (m *CredentialsManager) setJSON(namespace, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("config: encode %s/%s: %w", namespace, key, err)
	}
	return m.store.Set(namespace, key, raw)
}
