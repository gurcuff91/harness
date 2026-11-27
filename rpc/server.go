// Package rpc is the JSON-RPC frontend of Harness: the agent and its admin
// layer over stdio, for clients written in other languages. Design:
// docs/plans/2026-11-26-rpc-frontend-design.md.
//
// One client per process. The protocol is symmetric: the binary answers the
// client's requests and sends its own (`tool/call`), plus the notifications
// `event` and `tool/cancel`. Closing stdin shuts the process down — sessions,
// the agent and the stores are closed.
//
// stdout carries ONLY protocol messages; diagnostics go to the Logger (the CLI
// passes one that writes to stderr).
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// Option configures Run.
type Option func(*runConfig)

type runConfig struct {
	stdin  io.Reader
	stdout io.Writer
	logger logx.Logger
}

// WithStdin sets the stream requests are read from (default os.Stdin).
func WithStdin(r io.Reader) Option { return func(c *runConfig) { c.stdin = r } }

// WithStdout sets the stream replies, requests and notifications are written
// to (default os.Stdout).
func WithStdout(w io.Writer) Option { return func(c *runConfig) { c.stdout = w } }

// WithLogger sets what receives the frontend's diagnostics (dropped events,
// failures after a reply). Default logx.NewNilLogger(): silent.
func WithLogger(l logx.Logger) Option {
	return func(c *runConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// component is the name diagnostics are logged under.
const component = "rpc"

// connection states.
const (
	stateNew = iota
	stateInitializing
	stateReady
)

// eventBuffer is each session's watcher buffer.
const eventBuffer = 256

// Server is the state of one RPC connection. Operations (Op) receive it: it
// holds the agent and admin built by `initialize`, the open sessions and the
// connection.
type Server struct {
	conn   *jsonrpc.Conn
	logger logx.Logger
	ops    map[string]Op
	stop   context.CancelFunc // ends Run (agent/shutdown, a version mismatch)

	mu    sync.Mutex
	state int
	// initBuilding: an `initialize` is building the agent right now (so a
	// second one is refused as already initialized).
	initBuilding bool
	// storesRegistered: `initialize` registered a process-global settings or
	// credentials store, so a failure after it ends the process.
	storesRegistered bool
	agent            *agent.Agent
	admin            *admin.Admin
	sessions         map[string]*openSession
	closed           bool
}

// openSession is a session this connection opened and the pump that forwards
// its events.
type openSession struct {
	sess    *agent.Session
	watcher *agent.Watcher
	done    chan struct{} // closed when the pump has exited
}

// Run serves the protocol on stdin/stdout (or the given streams) until stdin
// closes (the client is gone: a clean shutdown, nil) or ctx is cancelled (also
// nil: Ctrl+C is not a failure). Then it shuts down: pending tool calls fail,
// every open session is closed, and so are the agent and the admin layer.
func Run(ctx context.Context, opts ...Option) error {
	cfg := runConfig{stdin: os.Stdin, stdout: os.Stdout, logger: logx.NewNilLogger()}
	for _, o := range opts {
		o(&cfg)
	}
	return runWith(ctx, cfg, nil)
}

// runWith is Run with the Server handed to onServer (when not nil) before it
// serves: the hook tests use to reach the agent without a public prompt op.
func runWith(ctx context.Context, cfg runConfig, onServer func(*Server)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &Server{
		conn:     jsonrpc.NewConn(cfg.stdin, cfg.stdout),
		logger:   cfg.logger,
		ops:      opIndex(),
		stop:     cancel,
		sessions: make(map[string]*openSession),
	}
	if onServer != nil {
		onServer(s)
	}
	err := s.conn.ServeWith(ctx, s.dispatch, s)
	s.shutdown()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// dispatch routes one incoming request or notification.
func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage, isNotification bool) (any, error) {
	if method == "initialize" {
		if err := checkProtocolVersion(params); err != nil {
			err.After = s.stop // the process exits after replying
			return nil, err
		}
	} else if !s.awaitReady(ctx) {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "not initialized: send `initialize` first"}
	}
	op, ok := s.ops[method]
	if !ok {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found: " + method}
	}
	res, err := op.call(ctx, s, params)
	if err != nil {
		return nil, rpcError(err)
	}
	if op.after != nil {
		return replied{res: res, run: func() { op.after(s) }}, nil
	}
	return res, nil
}

// requestStop ends Run (the reply of the operation that asked is already
// written).
func (s *Server) requestStop() { s.stop() }

// Received runs on the connection's reading goroutine, in arrival order and
// before the request's own goroutine starts: it marks `initialize` as begun, so
// a request pipelined right behind it (which runs concurrently and may reach
// dispatch first) waits for the agent instead of failing.
func (s *Server) Received(method string) {
	if method != "initialize" {
		return
	}
	s.mu.Lock()
	if s.state == stateNew {
		s.state = stateInitializing
	}
	s.mu.Unlock()
}

// awaitReady reports whether the connection is initialized. Requests run
// concurrently, so a client may pipeline `initialize` and the requests right
// behind it; while `initialize` is still building the agent those wait for it
// (bounded by ctx) instead of failing. A connection that was never asked to
// initialize (state new) fails at once.
func (s *Server) awaitReady(ctx context.Context) bool {
	for {
		s.mu.Lock()
		st := s.state
		s.mu.Unlock()
		switch st {
		case stateReady:
			return true
		case stateInitializing:
			select {
			case <-ctx.Done():
				return false
			case <-time.After(2 * time.Millisecond):
			}
		default:
			return false
		}
	}
}

func (s *Server) ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == stateReady
}

// checkProtocolVersion reads ONLY protocol_version (leniently: a client of
// another version may send fields this one does not know) and refuses any
// version but ProtocolVersion.
func checkProtocolVersion(params json.RawMessage) *jsonrpc.Error {
	var p struct {
		ProtocolVersion int `json:"protocol_version"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ProtocolVersion == ProtocolVersion {
		return nil
	}
	e := rpcErr("protocol_version", "unsupported protocol_version %d: this binary speaks %d", p.ProtocolVersion, ProtocolVersion)
	e.Data = map[string]any{"kind": "protocol_version", "supported": []int{ProtocolVersion}}
	return e
}

// shutdown closes everything this connection owns. Idempotent.
func (s *Server) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	open := make([]*openSession, 0, len(s.sessions))
	for _, o := range s.sessions {
		open = append(open, o)
	}
	s.sessions = make(map[string]*openSession)
	a, ad := s.agent, s.admin
	s.mu.Unlock()

	// Pending tool calls fail now (the connection is closed), so a turn stuck
	// on the client's answer ends; stopping the turns first makes it prompt.
	s.conn.Close()
	for _, o := range open {
		o.sess.Stop()
	}
	for _, o := range open {
		o.watcher.Close()
		<-o.done
		if err := o.sess.Close(); err != nil {
			s.logger.Warn(component, "close session", "session", o.sess.ID(), "error", err.Error())
		}
	}
	if a != nil {
		if err := a.Close(); err != nil {
			s.logger.Warn(component, "close agent", "error", err.Error())
		}
	}
	if ad != nil {
		if err := ad.Close(); err != nil {
			s.logger.Warn(component, "close admin", "error", err.Error())
		}
	}
}

// ── helpers for operations ───────────────────────────────────────────────

// Agent returns the agent built by `initialize`.
func (s *Server) Agent() *agent.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agent
}

// Admin returns the admin layer bound to the agent.
func (s *Server) Admin() *admin.Admin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admin
}

// session returns the open session id, or a not_found error.
func (s *Server) session(id string) (*agent.Session, error) {
	s.mu.Lock()
	o, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		return nil, rpcErr("not_found", "session %q is not open in this connection", id)
	}
	return o.sess, nil
}

// track registers a session this connection just opened (or reopened) and
// starts its event pump, once. The session's detail is returned for the reply.
func (s *Server) track(sess *agent.Session) agent.SessionDetail {
	s.mu.Lock()
	if _, ok := s.sessions[sess.ID()]; ok || s.closed {
		s.mu.Unlock()
		return sess.Detail()
	}
	id := sess.ID()
	w := sess.Watch(eventBuffer, func(e types.Event) {
		s.logger.Warn(component, "event dropped: client too slow", "session", id, "type", fmt.Sprint(e.Type))
	})
	o := &openSession{sess: sess, watcher: w, done: make(chan struct{})}
	s.sessions[id] = o
	s.mu.Unlock()

	go s.pump(o)
	return sess.Detail()
}

// pump sends one session's events as `event` notifications until its watcher
// closes (session/close, the session closing by itself, shutdown).
func (s *Server) pump(o *openSession) {
	defer close(o.done)
	id := o.sess.ID()
	for e := range o.watcher.Events() {
		payload, ok := types.WireEvent(e)
		if !ok {
			continue
		}
		payload["session_id"] = id
		if err := s.conn.Notify("event", payload); err != nil {
			s.logger.Warn(component, "send event", "session", id, "error", err.Error())
		}
	}
	// A session that closed by itself leaves the map; one closed through
	// session/close or shutdown has been removed already.
	s.mu.Lock()
	if cur, ok := s.sessions[id]; ok && cur == o {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
}

// untrack closes an open session: its pump stops and the session closes.
func (s *Server) untrack(id string) error {
	s.mu.Lock()
	o, ok := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if !ok {
		return rpcErr("not_found", "session %q is not open in this connection", id)
	}
	o.sess.Stop()
	// Close the SESSION first: it cancels its live sub-agents and emits their
	// subagent_stop (result "cancelled") while the watcher still exists, so the
	// client sees every sub-agent end. Closing the watcher first would drop those
	// events and leave ghosts in the client's list. Closing the session also
	// closes its watchers, which ends the pump.
	err := o.sess.Close()
	o.watcher.Close() // idempotent: a no-op after the session closed it
	<-o.done
	return err
}
