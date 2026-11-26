// Package admin is everything that does not need a live session: stored
// sessions, providers and their credentials, settings, MCP server
// configuration, memory and schedules. It is the counterpart of package agent,
// which operates live sessions.
//
// Every frontend of Harness (the CLI, the HTTP server, anything an SDK
// consumer writes) is a thin layer over these two packages: it decodes a
// request, calls admin or agent, and encodes the answer.
//
//	ad := admin.New()                                    // no agent: what the CLI uses
//	ad := admin.New(admin.WithActiveSessions(a.ActiveSessions))
//	ad.Providers.Connect(ctx, "anthropic", creds)
//	metas, _ := ad.Sessions.List(cwd)
//
// admin does not import package agent. It coordinates with running agents
// through shared state (a session re-reads its provider at every turn) and, for
// deleting or renaming a stored session, by asking which sessions are open
// (WithActiveSessions). A session open in ANOTHER process is not protected.
package admin

import (
	"errors"
	"fmt"
	"sync"

	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
)

// Errors. The server maps them to 404, 400, 409 and so on with errors.Is.
var (
	// ErrNotFound: the thing named does not exist (a provider, a session, a memory…).
	ErrNotFound = errors.New("not found")
	// ErrInvalid: the input is well-formed but not acceptable.
	ErrInvalid = errors.New("invalid")
	// ErrSessionActive: the session is open in this process; close it first.
	ErrSessionActive = errors.New("session is open")
	// ErrConflict: the request clashes with the current state.
	ErrConflict = errors.New("conflict")
	// ErrNotConfigured: the feature has no backing store here (e.g. no memory).
	ErrNotConfigured = errors.New("not configured")

	// ErrNotConnected: the provider exists but has no credentials / is not
	// reachable (HTTP 409 on the server).
	ErrNotConnected = errors.New("provider not connected")
	// ErrUpstream: a provider (or its OAuth endpoint) failed (HTTP 502).
	ErrUpstream = errors.New("upstream failure")
	// ErrUnprocessable: well-formed input whose content is invalid — a bad
	// thinking level, an invalid MCP server or custom provider, empty memory
	// content (HTTP 422).
	ErrUnprocessable = errors.New("unprocessable")

	// Provider sentinels, the same values package agent re-exports.
	ErrProviderNotFound     = providers.ErrNotFound
	ErrProviderNotRemovable = providers.ErrNotRemovable
	ErrProviderNameConflict = providers.ErrNameConflict
)

// marked is an error that keeps its inner message verbatim (so the text a
// server returned before stays byte-identical) while also matching a kind
// sentinel with errors.Is — and still everything the inner error wraps.
type marked struct {
	kind error
	err  error
}

func (m *marked) Error() string   { return m.err.Error() }
func (m *marked) Unwrap() []error { return []error{m.kind, m.err} }

// mark tags err with kind without changing its message.
func mark(kind, err error) error {
	if err == nil {
		return nil
	}
	return &marked{kind: kind, err: err}
}

func markf(kind error, format string, args ...any) error {
	return mark(kind, fmt.Errorf(format, args...))
}

// Admin groups one service per domain.
type Admin struct {
	Sessions  *Sessions
	Providers *Providers
	Settings  *Settings
	MCP       *MCP
	Memory    *Memory
	Schedules *Schedules

	mu      sync.Mutex
	closed  bool
	closers []func() error
}

// Option configures New. Plain With* names: this package has one kind of option.
type Option func(*options)

type options struct {
	sessions store.SessionStore
	memory   memory.Store
	active   func() []string
}

// WithSessionStore sets where stored sessions live. Default: the same file
// store agent.New uses (~/.harness/agent/sessions), falling back to memory.
func WithSessionStore(s store.SessionStore) Option { return func(c *options) { c.sessions = s } }

// WithMemory sets the memory store. Default: none (Memory methods return
// ErrNotConfigured, except that reads of an absent store are empty).
func WithMemory(m memory.Store) Option { return func(c *options) { c.memory = m } }

// WithActiveSessions tells admin which sessions are open in this process
// (typically agent.ActiveSessions), so deleting or renaming one is refused with
// ErrSessionActive. Without it nothing is checked.
func WithActiveSessions(fn func() []string) Option { return func(c *options) { c.active = fn } }

// New builds an Admin. Nothing is opened until a service needs it.
func New(opts ...Option) *Admin {
	var c options
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	a := &Admin{}
	a.Sessions = newSessions(a, &c)
	a.Providers = newProviders(a)
	a.Settings = newSettings(a)
	a.MCP = newMCP(a)
	a.Memory = newMemory(a, &c)
	a.Schedules = newSchedules(a)
	return a
}

func (a *Admin) addCloser(fn func() error) {
	a.mu.Lock()
	a.closers = append(a.closers, fn)
	a.mu.Unlock()
}

// Close releases what admin opened itself (a default store). Stores passed with
// With* belong to the caller and are left open.
func (a *Admin) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	cl := a.closers
	a.closers = nil
	a.mu.Unlock()
	var first error
	for _, fn := range cl {
		if err := fn(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
