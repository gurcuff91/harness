package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// slackJSON is the single source of truth for ~/.harness/slack.json.
// It holds both the auth credentials (from `harness slack login`) and the
// channel→session mappings (from the transport runtime). Both sides read and
// write through this struct so neither overwrites the other's fields.
type slackJSON struct {
	// Auth credentials — written by `harness slack login`.
	Workspace string `json:"workspace,omitempty"`
	XoxC      string `json:"xoxc,omitempty"`
	XoxD      string `json:"xoxd,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Team      string `json:"team,omitempty"`

	// Session mappings — written by the transport at runtime.
	Sessions map[string]string `json:"sessions,omitempty"`
}

// Credentials is the public view of the auth fields in slackJSON.
type Credentials struct {
	Workspace string
	XoxC      string
	XoxD      string
	UserID    string
	Team      string
}

// slackJSONPath returns ~/.harness/slack.json.
func slackJSONPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".harness", "slack.json"), nil
}

// readSlackJSON reads the file, returning a zero-value struct if absent.
func readSlackJSON(path string) (slackJSON, error) {
	var s slackJSON
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	_ = json.Unmarshal(data, &s)
	if s.Sessions == nil {
		s.Sessions = map[string]string{}
	}
	return s, nil
}

// writeSlackJSON persists the struct to path atomically (0600).
func writeSlackJSON(path string, s slackJSON) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ── Credentials API (used by login flow) ─────────────────────────────────

// LoadCredentials reads the auth fields from ~/.harness/slack.json.
// Returns nil (no error) if the file doesn't exist or has no credentials.
func LoadCredentials() (*Credentials, error) {
	path, err := slackJSONPath()
	if err != nil {
		return nil, err
	}
	s, err := readSlackJSON(path)
	if err != nil {
		return nil, err
	}
	if s.Workspace == "" || s.XoxC == "" || s.XoxD == "" {
		return nil, nil
	}
	return &Credentials{
		Workspace: s.Workspace,
		XoxC:      s.XoxC,
		XoxD:      s.XoxD,
		UserID:    s.UserID,
		Team:      s.Team,
	}, nil
}

// SaveCredentials updates only the auth fields in ~/.harness/slack.json,
// preserving any existing session mappings.
func SaveCredentials(c *Credentials) error {
	path, err := slackJSONPath()
	if err != nil {
		return err
	}
	s, err := readSlackJSON(path)
	if err != nil {
		return err
	}
	// Update only the credentials fields; sessions are untouched.
	s.Workspace = c.Workspace
	s.XoxC = c.XoxC
	s.XoxD = c.XoxD
	s.UserID = c.UserID
	s.Team = c.Team
	return writeSlackJSON(path, s)
}

// ── Store (session mapping) — replaces store.go's separate file logic ────

// store persists channel→session mappings in the shared ~/.harness/slack.json,
// updating only the sessions field and leaving credentials intact.
type store struct {
	mu   sync.Mutex
	path string
	data slackJSON
}

func openStore(path string) (*store, error) {
	if path == "" {
		var err error
		path, err = slackJSONPath()
		if err != nil {
			return nil, err
		}
	}
	s := &store{path: path}
	var err error
	s.data, err = readSlackJSON(path)
	if err != nil {
		return nil, err
	}
	if s.data.Sessions == nil {
		s.data.Sessions = map[string]string{}
	}
	return s, nil
}

func (s *store) sessionFor(channelID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.data.Sessions[channelID]
	return id, ok && id != ""
}

func (s *store) bind(channelID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Sessions[channelID] = sessionID
	return s.save()
}

func (s *store) unbind(channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Sessions, channelID)
	return s.save()
}

// save re-reads the file to pick up any credential changes made since open,
// then merges current sessions and writes back — so neither side loses data.
func (s *store) save() error {
	// Re-read to get the latest credentials (may have changed since openStore).
	current, err := readSlackJSON(s.path)
	if err != nil {
		// If the file disappeared, use what we have in memory.
		current = s.data
	}
	// Preserve credentials from disk; apply our in-memory sessions.
	current.Sessions = s.data.Sessions
	s.data = current
	return writeSlackJSON(s.path, current)
}
