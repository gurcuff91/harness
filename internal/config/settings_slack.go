package config

// ── Slack account (non-secret) ────────────────────────────────────────────
//
// The xoxc/xoxd session pair is a secret and lives in the credentials store
// (see CredentialsManager.SlackSession). Here: the workspace identity saved by
// `harness slack login` (channels/slack). The admin list and channel→session
// bindings of the removed Slack frontend (namespaces slack_admins and
// slack_sessions) are no longer read or written.
//
// Store layout:
//
//	slack / config → SlackConfig

const (
	nsSlack        = "slack"
	keySlackConfig = "config"
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

// DeleteSlackConfig removes the saved Slack workspace identity
// (workspace/user/team). Admins and channel bindings are untouched; deleting
// when none is saved is not an error.
func (m *SettingsManager) DeleteSlackConfig() error {
	return m.store.Delete(nsSlack, keySlackConfig)
}
