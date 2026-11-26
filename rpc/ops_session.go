package rpc

import (
	"context"
	"os"
	"strings"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/types"
)

// sessionOps open and close sessions. Every session opened here is tracked by
// the Server (Server.session finds it by id) and has its event pump started.
func sessionOps() []Op {
	return []Op{
		Define("session/new", "Creates a session and starts sending its events.", opSessionNew),
		Define("session/resume", "Reopens a stored session (or returns the open one) and starts sending its events.", opSessionResume),
		Define("session/fork", "Copies a session at this instant into a new one and starts sending its events.", opSessionFork),
		Define("session/import", "Imports a session export (a file path or the JSONL content) as a NEW session.", opSessionImport),
		Define("session/close", "Closes an open session (its events stop).", opSessionClose),
	}
}

// SessionOptions are the per-session options shared by new, resume and fork.
type SessionOptions struct {
	// EnabledProfiles: absent/null = unchanged (new: none; fork: the parent's),
	// names = exactly those, [] = remove all.
	EnabledProfiles *[]string `json:"enabled_profiles,omitempty"`
	// Directives are text particular to this session (never persisted).
	Directives []string `json:"directives,omitempty"`
	// Channels are the conversations the session listens to (never persisted).
	Channels []types.InputChannel `json:"channels,omitempty"`
}

func (o SessionOptions) options() []agent.SessionOption {
	var opts []agent.SessionOption
	if o.EnabledProfiles != nil {
		opts = append(opts, agent.SessionWithEnabledProfiles(*o.EnabledProfiles...))
	}
	if len(o.Directives) > 0 {
		opts = append(opts, agent.SessionWithDirectives(o.Directives...))
	}
	if len(o.Channels) > 0 {
		opts = append(opts, agent.SessionWithChannels(o.Channels...))
	}
	return opts
}

// SessionNewParams are the params of session/new.
type SessionNewParams struct {
	CWD   string `json:"cwd"`
	Model string `json:"model"`
	Name  string `json:"name,omitempty"`
	SessionOptions
}

func opSessionNew(_ context.Context, s *Server, p SessionNewParams) (agent.SessionDetail, error) {
	if p.Model == "" {
		return agent.SessionDetail{}, errInvalid("model is required")
	}
	sess, err := s.Agent().NewSession(p.CWD, p.Model, p.options()...)
	if err != nil {
		return agent.SessionDetail{}, err
	}
	if p.Name != "" {
		_ = sess.Rename(p.Name)
	}
	return s.track(sess), nil
}

// SessionIDParams carry a session id.
type SessionIDParams struct {
	ID string `json:"id"`
}

// SessionResumeParams are the params of session/resume.
type SessionResumeParams struct {
	ID string `json:"id"`
	SessionOptions
}

func opSessionResume(_ context.Context, s *Server, p SessionResumeParams) (agent.SessionDetail, error) {
	if p.ID == "" {
		return agent.SessionDetail{}, errInvalid("id is required")
	}
	sess, err := s.Agent().ResumeSession(p.ID, p.options()...)
	if err != nil {
		return agent.SessionDetail{}, err
	}
	return s.track(sess), nil
}

// SessionForkParams are the params of session/fork.
type SessionForkParams struct {
	ID string `json:"id"`
	SessionOptions
}

func opSessionFork(_ context.Context, s *Server, p SessionForkParams) (agent.SessionDetail, error) {
	if p.ID == "" {
		return agent.SessionDetail{}, errInvalid("id is required")
	}
	sess, err := s.Agent().ForkSession(p.ID, p.options()...)
	if err != nil {
		return agent.SessionDetail{}, err
	}
	return s.track(sess), nil
}

// SessionImportParams are the params of session/import: exactly one of Path (a
// file ending in .export.jsonl) or Content (the JSONL itself).
type SessionImportParams struct {
	Path     string               `json:"path,omitempty"`
	Content  string               `json:"content,omitempty"`
	CWD      string               `json:"cwd,omitempty"`
	Channels []types.InputChannel `json:"channels,omitempty"`
}

func opSessionImport(_ context.Context, s *Server, p SessionImportParams) (agent.SessionDetail, error) {
	if (p.Path == "") == (p.Content == "") {
		return agent.SessionDetail{}, errInvalid("session/import takes exactly one of path or content")
	}
	var opts []agent.SessionOption
	if len(p.Channels) > 0 {
		opts = append(opts, agent.SessionWithChannels(p.Channels...))
	}
	var sess *agent.Session
	var err error
	if p.Path != "" {
		if !strings.HasSuffix(p.Path, ".export.jsonl") {
			return agent.SessionDetail{}, errInvalid("path must be a *.export.jsonl file")
		}
		f, oerr := os.Open(p.Path)
		if oerr != nil {
			if os.IsNotExist(oerr) {
				return agent.SessionDetail{}, rpcErr("not_found", "export file %q not found", p.Path)
			}
			return agent.SessionDetail{}, errInvalid("open %s: %v", p.Path, oerr)
		}
		defer f.Close()
		sess, err = s.Agent().ImportSession(f, p.CWD, opts...)
	} else {
		sess, err = s.Agent().ImportSession(strings.NewReader(p.Content), p.CWD, opts...)
	}
	if err != nil {
		return agent.SessionDetail{}, err
	}
	return s.track(sess), nil
}

func opSessionClose(_ context.Context, s *Server, p SessionIDParams) (Empty, error) {
	return Empty{}, s.untrack(p.ID)
}
