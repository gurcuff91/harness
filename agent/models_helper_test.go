package agent

import (
	"context"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/store"
	"io"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/types"
)

// Models lists every model of every active provider — TEST ONLY. The model
// registry belongs to package admin (admin.Providers.Models); the agent's own
// tests just need a model to open a session on.
func (a *Agent) Models() []types.ModelListing {
	list, _ := admin.New().Providers.Models(context.Background(), "")
	return list
}

// ── test-only stand-ins for what moved to package admin ──────────────────

// ExportSession exports a session (live meta when open, else stored).
func (a *Agent) ExportSession(id string, w io.Writer) (int, error) {
	a.sessMu.Lock()
	live := a.activeSessions[id]
	a.sessMu.Unlock()
	if live != nil {
		return a.Admin().Sessions.ExportAs(live.Meta(), w)
	}
	return a.Admin().Sessions.Export(id, w)
}

// SessionProfiles returns the profile names bound to a session, live or stored.
func (a *Agent) SessionProfiles(id string) []string {
	a.sessMu.Lock()
	live := a.activeSessions[id]
	a.sessMu.Unlock()
	if live != nil {
		return append([]string(nil), live.store.Meta().Profiles...)
	}
	return a.Admin().Sessions.Profiles(id)
}

// ListAllSessions lists every stored session.
func (a *Agent) ListAllSessions() ([]store.SessionMeta, error) { return a.store.ListMetas("") }

// Memory is the agent's memory store.
func (a *Agent) Memory() memory.Store { return a.memStore }
