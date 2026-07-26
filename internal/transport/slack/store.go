package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// store persists the channel → harness session mapping to disk so conversations
// survive a restart. One session per channel (DM or channel where the bot is
// mentioned), exactly like the Telegram transport's chat → session mapping.
//
// File: ~/.harness/slack.json
type store struct {
	mu   sync.Mutex
	path string
	data storeData
}

type storeData struct {
	// Sessions maps Slack channel IDs (D… for DMs, C… for channels) to harness
	// session IDs. A channel keeps its context across restarts unless the session
	// is explicitly deleted.
	Sessions map[string]string `json:"sessions"`
}

// openStore loads the store from path (default ~/.harness/slack.json).
// A missing file yields an empty store — not an error.
func openStore(path string) (*store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".harness", "slack.json")
	}
	s := &store{path: path, data: storeData{Sessions: map[string]string{}}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data)
		if s.data.Sessions == nil {
			s.data.Sessions = map[string]string{}
		}
	}
	return s, nil
}

// sessionFor returns the harness session ID mapped to the given Slack channel,
// and whether one was found.
func (s *store) sessionFor(channelID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.data.Sessions[channelID]
	return id, ok && id != ""
}

// bind records a channel → session mapping and persists it.
func (s *store) bind(channelID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Sessions[channelID] = sessionID
	return s.save()
}

// unbind removes the channel mapping (session deleted or reset).
func (s *store) unbind(channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Sessions, channelID)
	return s.save()
}

func (s *store) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0700)
	return os.WriteFile(s.path, b, 0600)
}
