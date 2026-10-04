package telegram

import (
	"os"
	"time"

	"github.com/gurcuff91/harness/internal/config"
)

// store is the bot's persisted config and state, a thin layer over harness's
// settings and credentials managers: the bot token lives in the credentials
// store, the allowlist and chat→session bindings in the settings store
// (~/.harness/settings.json and credentials.json by default, or whatever
// stores an SDK embedder registered).
//
// Session bindings are scoped by working directory: each (project, chat) pair
// has its own independent session with the correct AGENTS.md, skills and
// working directory — same approach as the Slack transport.
type store struct {
	settings *config.SettingsManager
	cwd      string // current working directory — scopes all session lookups
}

// openStore returns the bot's store over the process-global managers, scoped
// to the current working directory. It never fails today; the error return
// is kept so callers stay unchanged.
func openStore() (*store, error) {
	cwd, _ := os.Getwd()
	return &store{settings: config.GetSettingsManager(), cwd: cwd}, nil
}

// ── Token ─────────────────────────────────────────────────────────────────

// SaveToken persists the bot token in the credentials store.
func SaveToken(token string) error {
	return config.GetCredentialsManager().SetTelegramToken(token)
}

// LoadToken reads the saved bot token. Returns "" (no error) if none was ever
// saved.
func LoadToken() (string, error) {
	return config.GetCredentialsManager().TelegramToken(), nil
}

// ── Allowlist ─────────────────────────────────────────────────────────────

func (s *store) allowed(chatID int64) bool { return s.settings.TelegramAllowed(chatID) }

func (s *store) allowlist() []int64 { return s.settings.TelegramAllowlist() }

func (s *store) pair(chatID int64) (bool, error) { return s.settings.PairTelegram(chatID) }

// unpair revokes chatID and drops its session bindings in every project.
func (s *store) unpair(chatID int64) (bool, error) { return s.settings.UnpairTelegram(chatID) }

// ── Sessions ──────────────────────────────────────────────────────────────

func (s *store) sessionFor(chatID int64) (string, bool) {
	return s.settings.TelegramSession(s.cwd, chatID)
}

// allSessions returns all (chatID → sessionID) mappings for the current
// working directory. Used at transport startup to pre-warm pumps so scheduled
// prompts never fire into a session with no active SSE consumer.
func (s *store) allSessions() map[int64]string { return s.settings.TelegramSessions(s.cwd) }

func (s *store) bind(chatID int64, sessionID string) error {
	return s.settings.BindTelegram(s.cwd, chatID, sessionID)
}

func (s *store) unbind(chatID int64) error { return s.settings.UnbindTelegram(s.cwd, chatID) }

// telegramSessionName returns the default name for new sessions created by the
// Telegram transport, e.g. "Telegram 2026-07-27 16:30".
func telegramSessionName() string {
	return "Telegram " + time.Now().Format("2006-01-02 15:04")
}
