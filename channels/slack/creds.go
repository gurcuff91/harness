package slack

import "github.com/gurcuff91/harness/internal/config"

// A Slack login is split by sensitivity across harness's two configuration
// stores (~/.harness/settings.json and credentials.json by default, or
// whatever stores an SDK embedder registered): the browser session pair
// (xoxc token + xoxd cookie) in the credentials store, the workspace identity
// (workspace, user, team) in the settings store. It is the same login
// `harness slack login` has always saved: these read and write the same keys.

// Credentials is the full saved Slack login.
type Credentials struct {
	Workspace string
	XoxC      string
	XoxD      string
	UserID    string
	Team      string
}

// LoadCredentials returns the saved login, or nil (no error) when there is
// none.
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
// the workspace identity to the settings store.
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

// DeleteCredentials signs out: removes the session pair and the workspace
// identity, so LoadCredentials returns nil. Not an error if nothing is saved.
func DeleteCredentials() error {
	if err := config.GetCredentialsManager().DeleteSlackSession(); err != nil {
		return err
	}
	return config.GetSettingsManager().DeleteSlackConfig()
}
