package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// store is the bot's on-disk config and state (~/.harness/telegram.json).
//
// Session structure: sessions[cwd][chatID] = sessionID
// This ensures each (project, chat) pair has its own independent session with
// the correct AGENTS.md, skills, and working directory context — same approach
// as the Slack transport.
type store struct {
	mu   sync.Mutex
	path string
	cwd  string // current working directory — scopes all session lookups
	data storeData
}

type storeData struct {
	Allowlist []int64                      `json:"allowlist"`
	Sessions  map[string]map[string]string `json:"sessions"` // cwd → chatID → sessionID
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
	cwd, _ := os.Getwd()
	s := &store{
		path: path,
		cwd:  cwd,
		data: storeData{Sessions: map[string]map[string]string{}},
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data)
		if s.data.Sessions == nil {
			s.data.Sessions = map[string]map[string]string{}
		}
	}
	return s, nil
}

// ── Allowlist ─────────────────────────────────────────────────────────────

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

func (s *store) allowlist() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, len(s.data.Allowlist))
	copy(out, s.data.Allowlist)
	return out
}

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
	// Remove from all CWD buckets.
	k := key(chatID)
	for cwd, m := range s.data.Sessions {
		delete(m, k)
		if len(m) == 0 {
			delete(s.data.Sessions, cwd)
		}
	}
	if !found {
		return false, nil
	}
	return true, s.save()
}

// ── Sessions ──────────────────────────────────────────────────────────────

func (s *store) sessionFor(chatID int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cwdMap, ok := s.data.Sessions[s.cwd]
	if !ok {
		return "", false
	}
	id, ok := cwdMap[key(chatID)]
	return id, ok && id != ""
}

func (s *store) bind(chatID int64, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Sessions[s.cwd] == nil {
		s.data.Sessions[s.cwd] = map[string]string{}
	}
	s.data.Sessions[s.cwd][key(chatID)] = sessionID
	return s.save()
}

func (s *store) unbind(chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cwdMap, ok := s.data.Sessions[s.cwd]; ok {
		delete(cwdMap, key(chatID))
		if len(cwdMap) == 0 {
			delete(s.data.Sessions, s.cwd)
		}
	}
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

// telegramSessionName returns the default name for new sessions created by the
// Telegram transport, e.g. "Telegram 2026-07-27 16:30".
func telegramSessionName() string {
	return "Telegram " + time.Now().Format("2006-01-02 15:04")
}
