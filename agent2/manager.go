package agent2

// ── SessionManager ──────────────────────────────────────────────────────

// SessionManager provides session lifecycle operations for transports.
// It wraps the Agent to offer list/delete/rename without exposing internals.
type SessionManager struct {
	agent  *Agent
}

// NewSessionManager creates a session manager backed by the given agent.
func NewSessionManager(a *Agent) *SessionManager {
	return &SessionManager{agent: a}
}

// List returns sessions filtered by working directory.
// TODO: implement when store exposes a list interface.
func (m *SessionManager) List(cwd string) ([]SessionMeta, error) {
	return nil, nil
}

// ListAll returns all sessions across all directories.
// TODO: implement when store exposes a list interface.
func (m *SessionManager) ListAll() ([]SessionMeta, error) {
	return nil, nil
}

// Delete removes a session by ID.
// TODO: implement when store exposes a delete interface.
func (m *SessionManager) Delete(sessionID string) error {
	return nil
}

// Rename sets a friendly name for a session.
func (m *SessionManager) Rename(sessionID, name string) error {
	session, err := m.agent.ResumeSession(sessionID)
	if err != nil {
		return err
	}
	defer session.Close()
	return session.Rename(name)
}

// NewSession creates a session (delegates to Agent).
func (m *SessionManager) NewSession(cwd string) (*Session, error) {
	return m.agent.NewSession(cwd)
}

// ResumeSession restores a session (delegates to Agent).
func (m *SessionManager) ResumeSession(sessionID string) (*Session, error) {
	return m.agent.ResumeSession(sessionID)
}
