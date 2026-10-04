package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// ── Telegram transport state (non-secret) ────────────────────────────────
//
// The bot token is a secret and lives in the credentials store (see
// CredentialsManager.TelegramToken). Here: which chats are allowed to use the
// bot, and which session each chat is bound to per project (cwd), so a chat
// gets an independent session — with the right AGENTS.md, skills and working
// directory — in every project the bot runs from.
//
// Store layout:
//
//	telegram_allowlist / <chatID>                → true
//	telegram_sessions  / entryKey(cwd, chatID)   → ChatBinding

const (
	nsTelegramAllowlist = "telegram_allowlist"
	nsTelegramSessions  = "telegram_sessions"
)

// ChatBinding maps one chat (or channel) to its session within one project.
// Shared by the Telegram and Slack transports.
type ChatBinding struct {
	CWD       string `json:"cwd"`
	ChatID    string `json:"chat_id"`
	SessionID string `json:"session_id"`
}

// TelegramAllowed reports whether chatID is allowed to use the bot.
func (m *SettingsManager) TelegramAllowed(chatID int64) bool {
	var ok bool
	return m.getJSON(nsTelegramAllowlist, chatKey(chatID), &ok) && ok
}

// TelegramAllowlist returns every allowed chat ID, sorted.
func (m *SettingsManager) TelegramAllowlist() []int64 {
	out := []int64{}
	for k := range listJSON[bool](m.store, nsTelegramAllowlist) {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PairTelegram allows chatID, reporting whether it was newly added (false if
// it was already allowed).
func (m *SettingsManager) PairTelegram(chatID int64) (added bool, err error) {
	err = m.store.SwapValue(nsTelegramAllowlist, chatKey(chatID), func(_ []byte, found bool) ([]byte, bool, error) {
		added = !found
		return []byte(`true`), added, nil
	})
	return added, err
}

// UnpairTelegram revokes chatID and drops its session bindings in EVERY
// project, reporting whether it had been allowed.
func (m *SettingsManager) UnpairTelegram(chatID int64) (removed bool, err error) {
	if _, found, err := m.store.Get(nsTelegramAllowlist, chatKey(chatID)); err != nil || !found {
		return false, err
	}
	if err := m.store.Delete(nsTelegramAllowlist, chatKey(chatID)); err != nil {
		return false, err
	}
	id := chatKey(chatID)
	for key, b := range listJSON[ChatBinding](m.store, nsTelegramSessions) {
		if b.ChatID == id {
			if err := m.store.Delete(nsTelegramSessions, key); err != nil {
				return true, err
			}
		}
	}
	return true, nil
}

// TelegramSession returns the session chatID is bound to in project cwd.
func (m *SettingsManager) TelegramSession(cwd string, chatID int64) (string, bool) {
	return m.chatSession(nsTelegramSessions, cwd, chatKey(chatID))
}

// TelegramSessions returns every chat→session binding in project cwd.
func (m *SettingsManager) TelegramSessions(cwd string) map[int64]string {
	out := map[int64]string{}
	for chat, sess := range m.chatSessions(nsTelegramSessions, cwd) {
		if id, err := strconv.ParseInt(chat, 10, 64); err == nil {
			out[id] = sess
		}
	}
	return out
}

// BindTelegram binds chatID to sessionID in project cwd (replacing any
// previous binding there).
func (m *SettingsManager) BindTelegram(cwd string, chatID int64, sessionID string) error {
	return m.bindChat(nsTelegramSessions, cwd, chatKey(chatID), sessionID)
}

// UnbindTelegram drops chatID's binding in project cwd.
func (m *SettingsManager) UnbindTelegram(cwd string, chatID int64) error {
	return m.store.Delete(nsTelegramSessions, entryKey(cwd, chatKey(chatID)))
}

// ── Chat-binding helpers (shared with Slack) ─────────────────────────────

func chatKey(chatID int64) string { return strconv.FormatInt(chatID, 10) }

// chatSession looks up one binding by its identity.
func (m *SettingsManager) chatSession(namespace, cwd, chatID string) (string, bool) {
	var b ChatBinding
	if !m.getJSON(namespace, entryKey(cwd, chatID), &b) || b.SessionID == "" {
		return "", false
	}
	return b.SessionID, true
}

// chatSessions returns every binding of namespace in project cwd, as
// chatID → sessionID.
func (m *SettingsManager) chatSessions(namespace, cwd string) map[string]string {
	out := map[string]string{}
	for _, b := range listJSON[ChatBinding](m.store, namespace) {
		if b.CWD == cwd && b.SessionID != "" {
			out[b.ChatID] = b.SessionID
		}
	}
	return out
}

// bindChat stores one binding under its identity key.
func (m *SettingsManager) bindChat(namespace, cwd, chatID, sessionID string) error {
	raw, err := json.Marshal(ChatBinding{CWD: cwd, ChatID: chatID, SessionID: sessionID})
	if err != nil {
		return fmt.Errorf("config: encode %s binding: %w", namespace, err)
	}
	return m.store.Set(namespace, entryKey(cwd, chatID), raw)
}
