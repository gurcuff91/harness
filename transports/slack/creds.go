package slack

import (
	"os"

	"github.com/gurcuff91/harness/internal/config"
)

// Slack's persisted state is split by sensitivity across harness's two
// configuration stores (~/.harness/settings.json and credentials.json by
// default, or whatever stores an SDK embedder registered):
//
//   - credentials store: the browser session pair (xoxc token + xoxd cookie)
//   - settings store:    the workspace identity (workspace, user, team), the
//     admin list, and channel→session bindings per working directory
//
// Session bindings are scoped by working directory: each (project, channel)
// pair has its own independent session with the correct AGENTS.md, skills
// and working directory context.

// Credentials is the full saved Slack login — its secret half (XoxC/XoxD)
// lives in the credentials store, the rest in the settings store.
type Credentials struct {
	Workspace string
	XoxC      string
	XoxD      string
	UserID    string
	Team      string
}

// ── Admin management ─────────────────────────────────────────────────────

// IsAdmin reports whether userID is in the admin list. Returns false when the
// list is empty (fail-closed — admins must be bootstrapped from the host via
// `harness slack admin add <userID>`).
func IsAdmin(userID string) (bool, error) {
	return config.GetSettingsManager().SlackIsAdmin(userID), nil
}

// AddAdmin adds userID to the admin list if not already present.
func AddAdmin(userID string) error {
	return config.GetSettingsManager().AddSlackAdmin(userID)
}

// RemoveAdmin removes userID from the admin list.
func RemoveAdmin(userID string) error {
	return config.GetSettingsManager().RemoveSlackAdmin(userID)
}

// ListAdmins returns the current admin list.
func ListAdmins() ([]string, error) {
	return config.GetSettingsManager().SlackAdmins(), nil
}

// ── Credentials API (used by login flow) ─────────────────────────────────

// LoadCredentials returns the saved login. Returns nil (no error) if there is
// no complete saved login (workspace + session pair).
func LoadCredentials() (*Credentials, error) {
	cfg, ok := config.GetSettingsManager().SlackConfig()
	if !ok || cfg.Workspace == "" {
		return nil, nil
	}
	sess, ok := config.GetCredentialsManager().SlackSession()
	if !ok {
		return nil, nil
	}
	return &Credentials{
		Workspace: cfg.Workspace,
		XoxC:      sess.XoxC,
		XoxD:      sess.XoxD,
		UserID:    cfg.UserID,
		Team:      cfg.Team,
	}, nil
}

// SaveCredentials saves a login: the session pair to the credentials store,
// the workspace identity to the settings store. Admins and session bindings
// are untouched.
func SaveCredentials(c *Credentials) error {
	if err := config.GetCredentialsManager().SetSlackSession(config.SlackSession{XoxC: c.XoxC, XoxD: c.XoxD}); err != nil {
		return err
	}
	return config.GetSettingsManager().SetSlackConfig(config.SlackConfig{
		Workspace: c.Workspace,
		UserID:    c.UserID,
		Team:      c.Team,
	})
}

// ── Store (session mapping) ───────────────────────────────────────────────

// store persists channel→session bindings for the current working directory,
// a thin layer over the settings manager.
type store struct {
	settings *config.SettingsManager
	cwd      string // current working directory — scopes all lookups
}

// openStore returns the session store over the process-global settings
// manager, scoped to the current working directory. It never fails today; the
// error return is kept so callers stay unchanged.
func openStore() (*store, error) {
	cwd, _ := os.Getwd()
	return &store{settings: config.GetSettingsManager(), cwd: cwd}, nil
}

func (s *store) sessionFor(channelID string) (string, bool) {
	return s.settings.SlackSessionFor(s.cwd, channelID)
}

// allSessions returns all (channelID → sessionID) mappings for the current
// working directory. Used at transport startup to pre-warm pumps so scheduled
// prompts never fire into a session with no active SSE consumer.
func (s *store) allSessions() map[string]string { return s.settings.SlackSessions(s.cwd) }

func (s *store) bind(channelID, sessionID string) error {
	return s.settings.BindSlack(s.cwd, channelID, sessionID)
}

func (s *store) unbind(channelID string) error { return s.settings.UnbindSlack(s.cwd, channelID) }
