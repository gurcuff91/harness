package admin

import (
	"slices"
	"sync"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// Sessions administers STORED sessions: listing, reading, renaming, deleting
// and exporting. It never opens a live session (that is package agent).
//
// Reads come from the store, which is append-only and written as a session
// runs, so they match a live session's log; a live session's meta (current
// model, thinking, stats) may be ahead of the stored one until its next
// persist — ExportAs lets the agent's owner supply the live meta.
type Sessions struct {
	ad     *Admin
	given  store.SessionStore
	active func() []string

	once sync.Once
	st   store.SessionStore
}

func newSessions(ad *Admin, c *options) *Sessions {
	return &Sessions{ad: ad, given: c.sessions, active: c.active}
}

// store returns the session store: the one given, else the default (file
// store, in-memory fallback), opened once and closed by Admin.Close.
func (s *Sessions) store() store.SessionStore {
	s.once.Do(func() {
		if s.given != nil {
			s.st = s.given
			return
		}
		if fs, err := store.NewFileStore(""); err == nil {
			s.st = fs
		} else {
			s.st = store.NewInMemoryStore()
		}
		s.ad.addCloser(s.st.Close)
	})
	return s.st
}

func (s *Sessions) isActive(id string) bool {
	return s.active != nil && slices.Contains(s.active(), id)
}

func notFound(id string) error { return markf(ErrNotFound, "session %s not found", id) }

// List returns the stored sessions of cwd, or of every directory when cwd is "".
func (s *Sessions) List(cwd string) ([]store.SessionMeta, error) {
	out, err := s.store().ListMetas(cwd)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []store.SessionMeta{}
	}
	return out, nil
}

// Get returns a session's stored meta (ErrNotFound when absent).
func (s *Sessions) Get(id string) (store.SessionMeta, error) {
	meta, found, err := s.store().LoadMeta(id)
	if err != nil {
		return store.SessionMeta{}, err
	}
	if !found {
		return store.SessionMeta{}, notFound(id)
	}
	return meta, nil
}

// Messages returns a window of a session's log (the zero window = all of it),
// the absolute index of the first message returned and the log's length.
// ErrNotFound when the session does not exist.
func (s *Sessions) Messages(id string, w store.MessageWindow) (msgs []types.Message, from, total int, err error) {
	if _, err := s.Get(id); err != nil {
		return nil, 0, 0, err
	}
	msgs, from, total, err = store.LoadMessagesWindow(s.store(), id, w)
	if msgs == nil && err == nil {
		msgs = []types.Message{}
	}
	return msgs, from, total, err
}

// Delete permanently removes a stored session. ErrNotFound when absent,
// ErrSessionActive when it is open in this process (see WithActiveSessions).
func (s *Sessions) Delete(id string) error {
	if s.isActive(id) {
		return sessionActive(id)
	}
	if _, err := s.Get(id); err != nil {
		return err
	}
	return s.store().DeleteSession(id)
}

// Rename sets a stored session's name. Same refusals as Delete.
func (s *Sessions) Rename(id, name string) error {
	if s.isActive(id) {
		return sessionActive(id)
	}
	if _, err := s.Get(id); err != nil {
		return err
	}
	return store.Rename(s.store(), id, name)
}

func sessionActive(id string) error {
	return markf(ErrSessionActive, "session %s is open in this process: close it first", id)
}

// ResetCount is how many times a session's history was reset (0 for a
// never-reset or unknown session).
func (s *Sessions) ResetCount(id string) int {
	meta, found, err := s.store().LoadMeta(id)
	if err != nil || !found {
		return 0
	}
	return meta.ResetCount
}

// Profiles returns the profile names bound to a session (nil if none or the
// session does not exist).
func (s *Sessions) Profiles(id string) []string {
	meta, found, err := s.store().LoadMeta(id)
	if err != nil || !found {
		return nil
	}
	return meta.Profiles
}
