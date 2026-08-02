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
	// Token is the bot token, saved via `harness telegram token <token>` so
	// Run doesn't require --token/TELEGRAM_BOT_TOKEN on every invocation —
	// same fallback pattern as Slack's Workspace/XoxC/XoxD in
	// ~/.harness/slack.json (see slack/creds.go).
	Token     string                       `json:"token,omitempty"`
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

// ── Token ─────────────────────────────────────────────────────────────────

// SaveToken persists the bot token to ~/.harness/telegram.json, preserving
// any existing allowlist/session mappings — the same read-modify-write
// pattern slack.SaveCredentials uses for its own auth fields.
func SaveToken(token string) error {
	st, err := openStore("")
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.data.Token = token
	return st.save()
}

// LoadToken reads the saved bot token from ~/.harness/telegram.json.
// Returns "" (no error) if none was ever saved.
func LoadToken() (string, error) {
	st, err := openStore("")
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.data.Token, nil
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

// allSessions returns all (chatID → sessionID) mappings for the current
// working directory. Used at transport startup to pre-warm pumps so scheduled
// prompts never fire into a session with no active SSE consumer.
func (s *store) allSessions() map[int64]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cwdMap, ok := s.data.Sessions[s.cwd]
	if !ok {
		return nil
	}
	out := make(map[int64]string, len(cwdMap))
	for k, v := range cwdMap {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil {
			out[id] = v
		}
	}
	return out
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
