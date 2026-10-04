package config

import "sort"

// ── Slack transport state (non-secret) ───────────────────────────────────
//
// The xoxc/xoxd session pair is a secret and lives in the credentials store
// (see CredentialsManager.SlackSession). Here: the workspace identity saved by
// `harness slack login`, the admin list, and channel→session bindings per
// project (cwd).
//
// Store layout:
//
//	slack          / config                        → SlackConfig
//	slack_admins   / <userID>                      → true
//	slack_sessions / entryKey(cwd, channelID)      → ChatBinding

const (
	nsSlack         = "slack"
	nsSlackAdmins   = "slack_admins"
	nsSlackSessions = "slack_sessions"
	keySlackConfig  = "config"
)

// SlackConfig is the non-secret half of a Slack login.
type SlackConfig struct {
	Workspace string `json:"workspace"`
	UserID    string `json:"user_id,omitempty"`
	Team      string `json:"team,omitempty"`
}

// SlackConfig returns the saved Slack workspace identity.
func (m *SettingsManager) SlackConfig() (SlackConfig, bool) {
	var c SlackConfig
	return c, m.getJSON(nsSlack, keySlackConfig, &c)
}

// SetSlackConfig saves the Slack workspace identity.
func (m *SettingsManager) SetSlackConfig(c SlackConfig) error {
	return m.setJSON(nsSlack, keySlackConfig, c)
}

// SlackIsAdmin reports whether userID is a Slack admin. With no admins
// configured, nobody is (fail-closed).
func (m *SettingsManager) SlackIsAdmin(userID string) bool {
	var ok bool
	return m.getJSON(nsSlackAdmins, userID, &ok) && ok
}

// SlackAdmins returns every admin user ID, sorted.
func (m *SettingsManager) SlackAdmins() []string {
	out := []string{}
	for id := range listJSON[bool](m.store, nsSlackAdmins) {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// AddSlackAdmin makes userID an admin (no-op if already one).
func (m *SettingsManager) AddSlackAdmin(userID string) error {
	return m.store.Set(nsSlackAdmins, userID, []byte(`true`))
}

// RemoveSlackAdmin revokes userID's admin rights (no-op if not one).
func (m *SettingsManager) RemoveSlackAdmin(userID string) error {
	return m.store.Delete(nsSlackAdmins, userID)
}

// SlackSessionFor returns the session channelID is bound to in project cwd.
func (m *SettingsManager) SlackSessionFor(cwd, channelID string) (string, bool) {
	return m.chatSession(nsSlackSessions, cwd, channelID)
}

// SlackSessions returns every channel→session binding in project cwd.
func (m *SettingsManager) SlackSessions(cwd string) map[string]string {
	return m.chatSessions(nsSlackSessions, cwd)
}

// BindSlack binds channelID to sessionID in project cwd (replacing any
// previous binding there).
func (m *SettingsManager) BindSlack(cwd, channelID, sessionID string) error {
	return m.bindChat(nsSlackSessions, cwd, channelID, sessionID)
}

// UnbindSlack drops channelID's binding in project cwd.
func (m *SettingsManager) UnbindSlack(cwd, channelID string) error {
	return m.store.Delete(nsSlackSessions, entryKey(cwd, channelID))
}
