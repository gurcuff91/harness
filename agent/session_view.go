package agent

import (
	"context"
	"runtime"

	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/version"
	"github.com/gurcuff91/harness/types"
)

// SessionView is a READ-ONLY view of the session a channel is bound for, for
// a channel's own read-only commands (Telegram's /info and /context). It has
// no method that changes the session: a channel is driven by people in a chat
// and must not be able to reconfigure it. Obtain it with SessionViewOf on the
// ctx of Channel.Listen.
type SessionView interface {
	// Info is the snapshot GET /api/sessions/{id}/info reports.
	Info() SessionSnapshot
	// Context is the context-window breakdown (GET /api/sessions/{id}/context).
	Context() ContextBreakdown
	// Quota is the subscription usage of the session's provider, through the
	// process's quota cache (60 s); an error for a provider without one.
	Quota(ctx context.Context) (*types.Quota, error)
}

// SessionSnapshot is a live session's state: what GET /api/sessions/{id}/info
// returns, also behind SessionView.Info. Every read is lock-free (safe during
// a running turn).
type SessionSnapshot struct {
	Version       string
	ID            string
	Name          string
	Model         string
	Thinking      string
	MaxIterations int
	Stats         types.SessionStats
	Channels      []ChannelInfo
	Busy          bool
	QueueDepth    int
	MCPConnected  int
	ScheduleCount int
	OS, Arch      string
	Terminal      string
}

// SessionViewOf returns the read-only view of the session a channel's Listen
// ctx belongs to; ok is false for any other ctx.
func SessionViewOf(ctx context.Context) (SessionView, bool) {
	s, isSession := ctx.Value(sessionCtxKey{}).(*Session)
	if !isSession || s == nil {
		return nil, false
	}
	return sessionView{s}, true
}

type sessionView struct{ s *Session }

func (v sessionView) Info() SessionSnapshot     { return v.s.Snapshot() }
func (v sessionView) Context() ContextBreakdown { return v.s.ContextBreakdown() }
func (v sessionView) Quota(ctx context.Context) (*types.Quota, error) {
	prov := providerPart(v.s.Model())
	q, _, err := providers.CachedQuota(ctx, prov, false)
	return q, err
}

// Snapshot returns the session's live state (see SessionSnapshot).
func (s *Session) Snapshot() SessionSnapshot {
	snap := SessionSnapshot{
		Version:       version.Version,
		ID:            s.ID(),
		Name:          s.Name(),
		Model:         s.Model(),
		Thinking:      s.Thinking(),
		MaxIterations: s.MaxIterations(),
		Stats:         s.Stats(),
		Channels:      s.Channels(),
		Busy:          s.IsBusy(),
		QueueDepth:    s.FollowUpCount(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
	if a := s.agent; a != nil {
		for _, st := range a.MCPStatuses() {
			if st.Connected {
				snap.MCPConnected++
			}
		}
		if store := a.Schedules(); store != nil {
			for _, sc := range store.List() {
				if sc.Owner == s.ID() {
					snap.ScheduleCount++
				}
			}
		}
		snap.Terminal = a.opts.Terminal
	}
	return snap
}

// providerPart is the provider of a "provider/model" string.
func providerPart(model string) string {
	for i := 0; i < len(model); i++ {
		if model[i] == '/' {
			return model[:i]
		}
	}
	return model
}
