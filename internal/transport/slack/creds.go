package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// slackJSON is the single source of truth for ~/.harness/slack.json.
// It holds both the auth credentials (from `harness slack login`) and the
// nested session mappings. Both sides read and write through this struct so
// neither overwrites the other's fields.
//
// Session structure: sessions[cwd][channelID] = sessionID
// This ensures each (project, channel) pair has its own independent session
// with the correct AGENTS.md, skills, and working directory context.
type slackJSON struct {
	// Auth credentials — written by `harness slack login`.
	Workspace string `json:"workspace,omitempty"`
	XoxC      string `json:"xoxc,omitempty"`
	XoxD      string `json:"xoxd,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Team      string `json:"team,omitempty"`

	// Admins is the list of Slack user IDs allowed to run state-changing
	// commands (/new, /stop, /compact, /thinking, /model). Read-only commands
	// (/help, /info, /context) are always public. Managed via
	// `harness slack admin <userID>`.
	Admins []string `json:"admins,omitempty"`

	// Session mappings: cwd → (channelID → sessionID).
	// A channel in a different working directory gets a separate session so the
	// agent always has the correct project context (AGENTS.md, skills, cwd).
	Sessions map[string]map[string]string `json:"sessions,omitempty"`
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
		s.Sessions = map[string]map[string]string{}
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

// ── Admin management ─────────────────────────────────────────────────────

// IsAdmin reports whether userID is in the admin list.
// Returns true if admins list is empty (open mode — no admins configured yet).
func IsAdmin(userID string) (bool, error) {
	path, err := slackJSONPath()
	if err != nil {
		return false, err
	}
	s, err := readSlackJSON(path)
	if err != nil {
		return false, err
	}
	if len(s.Admins) == 0 {
		return false, nil // no admins configured — nobody is admin
	}
	for _, a := range s.Admins {
		if a == userID {
			return true, nil
		}
	}
	return false, nil
}

// AddAdmin adds userID to the admin list if not already present.
func AddAdmin(userID string) error {
	path, err := slackJSONPath()
	if err != nil {
		return err
	}
	s, err := readSlackJSON(path)
	if err != nil {
		return err
	}
	for _, a := range s.Admins {
		if a == userID {
			return nil // already admin
		}
	}
	s.Admins = append(s.Admins, userID)
	return writeSlackJSON(path, s)
}

// RemoveAdmin removes userID from the admin list.
func RemoveAdmin(userID string) error {
	path, err := slackJSONPath()
	if err != nil {
		return err
	}
	s, err := readSlackJSON(path)
	if err != nil {
		return err
	}
	filtered := s.Admins[:0]
	for _, a := range s.Admins {
		if a != userID {
			filtered = append(filtered, a)
		}
	}
	s.Admins = filtered
	return writeSlackJSON(path, s)
}

// ListAdmins returns the current admin list.
func ListAdmins() ([]string, error) {
	path, err := slackJSONPath()
	if err != nil {
		return nil, err
	}
	s, err := readSlackJSON(path)
	if err != nil {
		return nil, err
	}
	return s.Admins, nil
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

// ── Store (session mapping) ───────────────────────────────────────────────

// store persists channel→session mappings in the shared ~/.harness/slack.json
// under the current working directory key, updating only the sessions field
// and leaving credentials intact.
//
// Lookup structure: sessions[cwd][channelID] = sessionID
type store struct {
	mu   sync.Mutex
	path string
	cwd  string    // current working directory — scopes all lookups
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
	cwd, _ := os.Getwd()
	s := &store{path: path, cwd: cwd}
	var err error
	s.data, err = readSlackJSON(path)
	if err != nil {
		return nil, err
	}
	if s.data.Sessions == nil {
		s.data.Sessions = map[string]map[string]string{}
	}
	return s, nil
}

func (s *store) sessionFor(channelID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cwdMap, ok := s.data.Sessions[s.cwd]
	if !ok {
		return "", false
	}
	id, ok := cwdMap[channelID]
	return id, ok && id != ""
}

func (s *store) bind(channelID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Sessions[s.cwd] == nil {
		s.data.Sessions[s.cwd] = map[string]string{}
	}
	s.data.Sessions[s.cwd][channelID] = sessionID
	return s.save()
}

func (s *store) unbind(channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cwdMap, ok := s.data.Sessions[s.cwd]; ok {
		delete(cwdMap, channelID)
		if len(cwdMap) == 0 {
			delete(s.data.Sessions, s.cwd)
		}
	}
	return s.save()
}

// save re-reads the file to pick up any credential changes made since open,
// then merges current sessions and writes back — so neither side loses data.
func (s *store) save() error {
	current, err := readSlackJSON(s.path)
	if err != nil {
		current = s.data
	}
	// Preserve credentials from disk; merge our in-memory sessions into it.
	// We only own the s.cwd bucket — other CWDs on disk are untouched.
	if current.Sessions == nil {
		current.Sessions = map[string]map[string]string{}
	}
	if s.data.Sessions[s.cwd] != nil {
		current.Sessions[s.cwd] = s.data.Sessions[s.cwd]
	} else {
		delete(current.Sessions, s.cwd)
	}
	s.data = current
	return writeSlackJSON(s.path, current)
}
