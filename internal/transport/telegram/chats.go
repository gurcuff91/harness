package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// store is the bot's on-disk config and state (~/.harness/telegram.json):
//   - Allowlist: the chat ids allowed to talk to the bot (managed via
//     `harness telegram pair/unpair`)
//   - Sessions:  chat id → harness session id, so a chat's conversation survives
//     a restart (Telegram chat ids are stable)
type store struct {
	mu   sync.Mutex
	path string
	data storeData
}

type storeData struct {
	Allowlist []int64           `json:"allowlist"`
	Sessions  map[string]string `json:"sessions"`
}

// openStore loads the config from path (default ~/.harness/telegram.json). A
// missing file yields an empty store.
func openStore(path string) (*store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("telegram: home dir: %w", err)
		}
		path = filepath.Join(home, ".harness", "telegram.json")
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

// ── Allowlist ─────────────────────────────────────────────────────────────

// allowed reports whether a chat is on the allowlist.
func (s *store) allowed(chatID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.data.Allowlist {
		if id == chatID {
			return true
		}
	}
	return false
}

// allowlist returns a copy of the allowed chat ids.
func (s *store) allowlist() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, len(s.data.Allowlist))
	copy(out, s.data.Allowlist)
	return out
}

// pair adds a chat to the allowlist (no-op if already present) and persists.
// Returns whether it was newly added.
func (s *store) pair(chatID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.data.Allowlist {
		if id == chatID {
			return false, nil
		}
	}
	s.data.Allowlist = append(s.data.Allowlist, chatID)
	return true, s.save()
}

// unpair removes a chat from the allowlist AND drops its session mapping (forget
// the chat entirely). Returns whether it was present.
func (s *store) unpair(chatID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	kept := s.data.Allowlist[:0]
	for _, id := range s.data.Allowlist {
		if id == chatID {
			found = true
			continue
		}
		kept = append(kept, id)
	}
	s.data.Allowlist = kept
	delete(s.data.Sessions, key(chatID))
	if !found {
		return false, nil
	}
	return true, s.save()
}

// ── Sessions ──────────────────────────────────────────────────────────────

// sessionFor returns the stored session id for a chat, and whether one exists.
func (s *store) sessionFor(chatID int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.data.Sessions[key(chatID)]
	return id, ok
}

// bind records a chat→session mapping and persists it.
func (s *store) bind(chatID int64, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Sessions[key(chatID)] = sessionID
	return s.save()
}

// unbind removes a chat's session mapping (e.g. on /new) and persists.
func (s *store) unbind(chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Sessions, key(chatID))
	return s.save()
}

// save writes the config to disk (caller holds s.mu).
func (s *store) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0600)
}

func key(chatID int64) string { return strconv.FormatInt(chatID, 10) }
