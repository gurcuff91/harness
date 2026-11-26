package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/version"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/mcp"
	"github.com/gurcuff91/harness/types"
)

// sseClientBufferSize is the per-client SSE event channel capacity
// (the buffer of each agent.Watcher handleEvents opens). A turn with
// thinking:high and a long response can emit thousands of small delta events
// (one per streamed token/fragment); if the consumer (the render loop on the
// other end of the connection) falls behind a burst, this is how much
// slack it gets before a delta is dropped (control events like turn_end/stop
// get a bounded blocking retry instead — see agent.Session.Watch).
// 4096 is generous headroom for that burst (each event is at most a few
// hundred bytes, so worst case this is a few MB — negligible) without being
// unbounded. Matches tuiClientEventBufferSize on the TUI's consuming side
// (internal/tui/client.go) — the two ends of the same pipe should
// have comparable slack, not one starving the other.
const sseClientBufferSize = 4096

// Server is the HTTP/SSE server for the agent harness.
type Server struct {
	agent     *agent.Agent
	admin     *admin.Admin // the agent's admin layer: stored sessions, providers, settings, MCP, memory, schedules
	logger    logx.Logger  // never nil — defaults to logx.NewNilLogger()
	frontend  string       // name of the calling frontend
	keepAgent bool         // Close leaves the agent open (it belongs to the caller)

	secret        string // when set, every request but the open paths must carry it (see secretMiddleware)
	proxyMaxBody  int64  // request-body cap of /api/providers/{name}/proxy/*; 0 = DefaultProxyMaxBodyBytes
	importMaxBody int64  // request-body cap of POST /api/sessions/import; 0 = DefaultImportMaxBodyBytes

	// openMu guards the server's OWN proxy map against two concurrent opens of
	// the same id each building a proxy (the agent already yields one live
	// Session per id; this keeps one proxy per Session).
	openMu sync.Mutex

	profMu   sync.RWMutex
	addr     string // resolved listen address (set in Serve)
	mu       sync.RWMutex
	sessions map[string]*SessionProxy

	// Graceful shutdown plumbing (set in Serve).
	httpSrv   *http.Server
	listener  net.Listener
	closeOnce sync.Once
	closeErr  error
}

// ServerOptions configures the HTTP server.
type ServerOptions struct {
	// Logger receives request/lifecycle log lines. Defaults to
	// logx.NewNilLogger() (silent) when left nil — the same default Run's
	// WithLogger option falls back to. Pass logx.NewNilLogger() explicitly (as
	// every frontend's own in-process server does) when something ELSE is
	// already logging and this server must stay silent.
	Logger   logx.Logger
	Frontend string // name of the calling frontend (e.g. "tui", "acp", "cli"); empty defaults to "server"

	// KeepAgentOpen makes Close leave the agent running: it closes this
	// server's sessions and HTTP listener, but not the agent's MCP, memory,
	// scheduler or store — for a server fronting an agent it doesn't own.
	// Default false: the server closes its agent (server.Run's semantics).
	KeepAgentOpen bool

	// ProxyMaxBodyBytes caps the request body of the provider proxy
	// (/api/providers/{name}/proxy/*); a larger one is answered 413. Zero means
	// DefaultProxyMaxBodyBytes (32 MiB).
	ProxyMaxBodyBytes int64

	// Secret, when not empty, makes the server require it on every request but
	// GET /api/server and the API docs: in the X-Harness-Sec header or as an
	// "Authorization: Bearer" token (401 otherwise). Empty = no authentication
	// (what an in-process server listening on localhost for its own frontend
	// uses). GenerateServerSec makes a good one.
	Secret string

	// ImportMaxBodyBytes caps the body of POST /api/sessions/import (a whole
	// session export); a larger one is answered 413. Zero means
	// DefaultImportMaxBodyBytes (1 GiB).
	ImportMaxBodyBytes int64
}

// NewServer creates an HTTP server wrapping the agent.
func NewServer(a *agent.Agent, opts ServerOptions) *Server {
	frontend := opts.Frontend
	if frontend == "" {
		frontend = "server"
	}
	logger := opts.Logger
	if logger == nil {
		logger = logx.NewNilLogger()
	}
	var ad *admin.Admin
	if a != nil {
		ad = a.Admin()
	}
	return &Server{
		agent:         a,
		admin:         ad,
		logger:        logger,
		frontend:      frontend,
		keepAgent:     opts.KeepAgentOpen,
		secret:        opts.Secret,
		proxyMaxBody:  opts.ProxyMaxBodyBytes,
		importMaxBody: opts.ImportMaxBodyBytes,
		sessions:      make(map[string]*SessionProxy),
	}
}

// handler builds the router with all routes and middleware. Both the standalone
// and internal servers share it so the route table lives in exactly one place.
func (s *Server) handler() http.Handler {
	r := chi.NewRouter()

	// Middleware — requestLogger always runs; with the default NilLogger it's
	// a no-op call per request, not a conditional branch (see requestLogger's
	// doc comment for why this simplification is safe).
	r.Use(requestLogger(s.logger))
	r.Use(corsMiddleware)
	if s.secret != "" {
		r.Use(secretMiddleware(s.secret))
	}

	// Routes
	r.Get("/api/server", s.handleServerInfo)
	r.Get("/api/docs", s.handleDocs)
	r.Get("/api/docs/openapi.json", s.handleOpenAPISpec)
	r.Get("/api/settings", s.handleSettings)
	r.Patch("/api/settings", s.handlePatchSettings)
	r.Get("/api/settings/mcp", s.handleListMCPServers)
	r.Put("/api/settings/mcp/{name}", s.handlePutMCPServer)
	r.Delete("/api/settings/mcp/{name}", s.handleDeleteMCPServer)
	r.Get("/api/mcp/status", s.handleMCPStatus)
	r.Get("/api/settings/provider", s.handleListCustomProviders)
	r.Put("/api/settings/provider/{name}", s.handlePutCustomProvider)
	r.Delete("/api/settings/provider/{name}", s.handleDeleteCustomProvider)
	r.Post("/api/settings/provider/reload", s.handleReloadCustomProviders)
	r.Get("/api/memories", s.handleListMemories)
	r.Get("/api/memories/{slug}", s.handleGetMemory)
	r.Put("/api/memories/{slug}", s.handlePutMemory)
	r.Delete("/api/memories/{slug}", s.handleDeleteMemory)
	r.Get("/api/schedules", s.handleListSchedules)
	r.Get("/api/providers", s.handleProviders)
	r.Post("/api/providers/{name}/connect", s.handleConnectProvider)
	r.Post("/api/providers/{name}/disconnect", s.handleDisconnectProvider)
	r.Get("/api/providers/{name}/quota", s.handleProviderQuota)
	r.HandleFunc("/api/providers/{name}/proxy/*", s.handleProviderProxy)
	r.Post("/api/oauth/{provider}", s.handleOAuth)
	r.Get("/api/models", s.handleModels)
	r.Get("/api/models/{provider}/*", s.handleModel)
	r.Get("/api/sessions", s.handleListSessions)
	r.Post("/api/sessions", s.handleCreateSession)
	r.Get("/api/sessions/{id}", s.handleGetSession)
	r.Delete("/api/sessions/{id}", s.handleDeleteSession)
	r.Post("/api/sessions/{id}/close", s.handleCloseSession)
	r.Post("/api/sessions/{id}/resume", s.handleResumeSession)
	r.Post("/api/sessions/{id}/fork", s.handleForkSession)
	r.Get("/api/sessions/{id}/export", s.handleExportSession)
	r.Post("/api/sessions/import", s.handleImportSession)
	r.Post("/api/sessions/{id}/prompt", s.handlePrompt)
	r.Post("/api/sessions/{id}/ask", s.handleAsk)
	r.Get("/api/sessions/{id}/events", s.handleEvents)
	r.Get("/api/sessions/{id}/commands", s.handleListCommands)
	r.Post("/api/sessions/{id}/commands", s.handleExecCommand)
	r.Get("/api/sessions/{id}/messages", s.handleGetMessages)
	r.Get("/api/sessions/{id}/messages/{index}/images/{n}", s.handleMessageImage)
	r.Post("/api/sessions/{id}/stop", s.handleStopSession)
	r.Get("/api/sessions/{id}/info", s.handleSessionInfo)
	r.Get("/api/sessions/{id}/context", s.handleSessionContext)
	r.Get("/api/sessions/{id}/sysprompt", s.handleSessionSystemPrompt)
	r.Get("/api/sessions/{id}/tools", s.handleListTools)

	// TEMPORARY diagnostic endpoint (not net/http/pprof's DefaultServeMux
	// auto-registration — that only wires up on import side effects, and this
	// server uses chi, not http.DefaultServeMux, so the handlers are mounted
	// explicitly here). Lets us pull a goroutine dump from a live, hung process
	// over loopback HTTP (127.0.0.1-only internal server) without attaching a
	// debugger — safe, doesn't touch the process. Remove once the mid-turn
	// freeze investigation is done.
	r.HandleFunc("/debug/pprof/*", pprof.Index)
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return r
}

// Serve runs the HTTP server on an already-open listener. This is the single
// serving entry point. Callers open the listener themselves (net.Listen), which
// guarantees the port is accepting connections the moment Listen returns — no
// close-then-reopen race, no readiness polling. For a fixed address, do
// net.Listen("tcp", addr) then Serve(l).
func (s *Server) Serve(l net.Listener) error {
	addr := l.Addr().String()
	s.mu.Lock()
	s.addr = addr
	s.listener = l
	s.mu.Unlock()
	s.logger.Info("server", "listening", "addr", addr)

	// Serve() itself (past this point) is the one legitimate unlocked read of
	// httpSrv: Close() only ever calls Shutdown() on it after this field is
	// set, and there is exactly one Serve() call per Server (one process, one
	// listener) — so there is no concurrent WRITE for this specific read to
	// race with, only Close()'s read, which the lock below already orders
	// after this write.
	httpSrv := &http.Server{Handler: s.handler()}
	s.mu.Lock()
	s.httpSrv = httpSrv
	s.mu.Unlock()
	return httpSrv.Serve(l)
}

// Addr returns the address the server is listening on ("host:port"), or ""
// if it isn't serving yet. Set as soon as Serve has its listener.
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.addr
}

// Start binds addr ("127.0.0.1:0" for loopback + an OS-assigned port; "" means
// the same) and serves the agent in the background, returning once the
// server is bound and accepting — so srv.Addr() is valid immediately, ready
// to hand to frontends (acp.Run(ctx, srv), …), which all share it.
// The caller owns the server: Close it when done (by default that also
// closes the agent — see ServerOptions.KeepAgentOpen).
func Start(a *agent.Agent, addr string, opts ServerOptions) (*Server, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := NewServer(a, opts)
	// Pre-set addr so Addr() is valid the moment Start returns, before the
	// Serve goroutine has run.
	srv.mu.Lock()
	srv.addr = l.Addr().String()
	srv.mu.Unlock()
	go srv.Serve(l) //nolint:errcheck
	return srv, nil
}

// Close performs a graceful shutdown: closes all active sessions (flushing
// their stores), closes the agent (MCP subprocesses, memory DB, scheduler
// engine, session store), and shuts down the HTTP server. Idempotent — safe
// to call multiple times. The first call does the work; subsequent calls
// return the same error.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		// 1. Close all active sessions — flush stores + unregister from agent.
		s.mu.RLock()
		var proxies []*SessionProxy
		for _, p := range s.sessions {
			proxies = append(proxies, p)
		}
		s.mu.RUnlock()
		for _, p := range proxies {
			_ = p.session.Close()
		}

		// 2. Close the agent — MCP, memory, scheduler, store — unless it
		// belongs to someone else (KeepAgentOpen).
		if !s.keepAgent {
			s.closeErr = s.agent.Close()
		}

		// 3. Graceful HTTP shutdown with a short deadline so in-flight
		// requests (e.g. SSE streams) are given a chance to close. httpSrv is
		// set by Serve() right before it blocks in httpSrv.Serve(l) — if Close()
		// races in fast enough to run before that assignment (e.g. a caller
		// cancels its context the instant Serve() is scheduled, before it's had
		// a chance to run at all), there is nothing to shut down yet; that's a
		// caller-ordering issue outside this method's control, not something
		// the lock below needs to paper over.
		s.mu.RLock()
		httpSrv := s.httpSrv
		s.mu.RUnlock()
		if httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := httpSrv.Shutdown(ctx); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}

	})
	return s.closeErr
}

// --- Handlers ---

type createSessionRequest struct {
	Model string `json:"model"`
	CWD   string `json:"cwd"`
	Name  string `json:"name,omitempty"` // optional initial name; default: "New Session <date>"
	// Profiles binds the new session to registered profiles by name
	// (persisted in its meta — see profiles.Profile). Unknown names → 400.
	// Absent = none; [] = none too.
	Profiles *[]string `json:"profiles,omitempty"`
	// Directives is instruction text for THIS session's system prompt only
	// (agent.SessionWithDirectives): after the agent's and the profiles', never
	// stored. Pass it again on every open that should carry it.
	Directives []string `json:"directives,omitempty"`
	// Channels are the external conversations the session listens to while it
	// is open (agent.SessionWithChannels) — never persisted. A conversation
	// another live session already listens to → 409; an unregistered type or
	// an unresolvable ref → 400.
	Channels []types.InputChannel `json:"channels,omitempty"`
}

// resumeSessionRequest is the OPTIONAL body of POST /api/sessions/{id}/resume.
type resumeSessionRequest struct {
	// Profiles REPLACES the session's persisted binding: absent or null =
	// unchanged, [] = remove them all, names = exactly those. Unknown names
	// → 400. If the session is already active, a changed binding is applied
	// at its next turn boundary.
	Profiles *[]string `json:"profiles,omitempty"`
	// Directives for the reopened session's system prompt only (not stored).
	// Ignored when the session is already open: it keeps its prompt.
	Directives []string `json:"directives,omitempty"`
	// Channels to listen to while the reopened session is open (see
	// createSessionRequest.Channels). Asking an already-open session for
	// channels it does not listen to → 400.
	Channels []types.InputChannel `json:"channels,omitempty"`
}

// serverInfo is returned by GET /api/server.
type serverInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Frontend  string `json:"frontend"`
	URL       string `json:"url"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// Static fields populated once at init.
var (
	staticServerName      = "harness"
	staticServerVersion   = version.Version
	staticServerStartedAt = time.Now().UTC().Format(time.RFC3339)
	staticServerCWD       string
	staticServerPID       = os.Getpid()
)

func init() {
	var err error
	staticServerCWD, err = os.Getwd()
	if err != nil {
		staticServerCWD = "."
	}
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	addr := s.addr
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, serverInfo{
		Name:      staticServerName,
		Version:   staticServerVersion,
		Frontend:  s.frontend,
		URL:       "http://" + addr,
		CWD:       staticServerCWD,
		PID:       staticServerPID,
		StartedAt: staticServerStartedAt,
	})
}

// handleMCPStatus reports the live connection status of each configured MCP
// server (connected? tool count? error?). This is how clients surface MCP
// health without the agent ever writing to stdout. Returns [] when MCP is off.
func (s *Server) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	statuses := s.agent.MCPStatuses()
	if statuses == nil {
		statuses = []mcp.Status{}
	}
	writeJSON(w, http.StatusOK, statuses)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required", nil)
		return
	}

	var opts []agent.SessionOption
	if req.Profiles != nil {
		opts = append(opts, agent.SessionWithEnabledProfiles(*req.Profiles...))
	}
	if len(req.Directives) > 0 {
		opts = append(opts, agent.SessionWithDirectives(req.Directives...))
	}
	if len(req.Channels) > 0 {
		opts = append(opts, agent.SessionWithChannels(req.Channels...))
	}
	sess, err := s.agent.NewSession(req.CWD, req.Model, opts...)
	if err != nil {
		def := http.StatusInternalServerError
		if errors.Is(err, agent.ErrUnknownProfile) {
			def = http.StatusBadRequest
		}
		writeErr(w, channelErrStatus(err, def), err)
		return
	}

	// Apply optional initial name (frontend-supplied, e.g. "Slack 2026-07-27").
	if req.Name != "" {
		_ = sess.Rename(req.Name)
	}

	// Thinking level: NewSession above already resolved it correctly via the
	// calling Agent's own a.thinkingLevel (agent.New's single entry point for
	// every caller — CLI, TUI, and any SDK embedder's AgentWithThinking(...)).
	// A second, unconditional re-application of the operator's global
	// ~/.harness/settings.json thinking level used to live here — it silently
	// discarded an embedding Agent's own explicitly-configured level (e.g.
	// AgentWithThinking("low") got overwritten by a global "high") the moment
	// a session was created via this endpoint, with no error or log anywhere.
	// model never got an equivalent second pass here, which was the tell:
	// this was leftover from before agent.New centralized the fallback, not a
	// deliberate default-application step. Removed — trust NewSession's own
	// resolution, which already falls back to the global setting exactly
	// when the Agent itself wasn't configured with one.

	s.mu.Lock()
	s.sessions[sess.ID()] = newSessionProxy(sess, s.logger)
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, liveDetail(sess))
}

// handleCloseSession closes an active session (flushes store, disconnects SSE clients).
func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	s.mu.Lock()
	proxy, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
		proxy.close()
	}
	s.mu.Unlock()

	if !ok {
		writeError(w, http.StatusNotFound, "session not active", nil)
		return
	}

	writeStatus(w, http.StatusOK, "closed", "")
}

// handleResumeSession reactivates a persisted session.
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}
	proxy.session.Stop()
	writeStatus(w, http.StatusOK, "stopped", "")
}

// parseMessageWindow validates GET /messages' query parameters. It returns a
// client-facing message (and a zero window) on the first problem.
func parseMessageWindow(from string, hasFrom bool, compactions string, hasCompactions bool) (store.MessageWindow, string) {
	var win store.MessageWindow
	if hasFrom && hasCompactions {
		return win, "from and compactions are mutually exclusive"
	}
	if hasFrom {
		n, err := strconv.Atoi(from)
		if err != nil || n < 0 {
			return win, "from must be a non-negative integer"
		}
		win.From = n
	}
	if hasCompactions {
		k, err := strconv.Atoi(compactions)
		if err != nil || k < 1 {
			return win, "compactions must be a positive integer"
		}
		win.Compactions = k
	}
	return win, ""
}

// handleMessageImage serves the n-th image (0-based, in message order) of the
// message at absolute history index {index} as raw bytes with its own
// Content-Type — what the message_index + images of received_prompt /
// follow_up_start point at, and what a UI rendering the history can use
// instead of decoding base64 from GET /messages. Works for active and
// persisted sessions.
//
// The history is append-only, so (session, index, n) always names the same
// bytes and is cached as immutable — EXCEPT across a /reset, which empties
// the log so indices are reused. The ETag carries the session's created_at
// plus a content hash, so a conditional request after a reset never matches —
// and the URL clients get carries the epoch too (?e=), see sessionEpoch.
func (s *Server) handleMessageImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	index, err1 := strconv.Atoi(chi.URLParam(r, "index"))
	n, err2 := strconv.Atoi(chi.URLParam(r, "n"))
	if err1 != nil || err2 != nil || index < 0 || n < 0 {
		writeError(w, http.StatusBadRequest, "index and n must be non-negative integers", nil)
		return
	}

	var msgs []types.Message
	s.mu.RLock()
	proxy, active := s.sessions[id]
	s.mu.RUnlock()
	if active {
		msgs = proxy.session.AllMessages()
	} else if all, _, _, err := s.admin.Sessions.Messages(id, store.MessageWindow{}); err == nil {
		msgs = all
	} else {
		writeError(w, http.StatusNotFound, "session not found", nil)
		return
	}
	if index >= len(msgs) {
		writeError(w, http.StatusNotFound, "message not found", nil)
		return
	}
	var img *types.ImageData
	seen := 0
	for _, p := range msgs[index].Parts {
		if p.Image == nil {
			continue
		}
		if seen == n {
			img = p.Image
			break
		}
		seen++
	}
	if img == nil {
		writeError(w, http.StatusNotFound, "image not found", nil)
		return
	}
	data, err := base64.StdEncoding.DecodeString(img.Base64)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stored image is not valid base64", nil)
		return
	}
	sum := sha256.Sum256(data)
	etag := fmt.Sprintf(`"%s-%x"`, s.sessionEpoch(id), sum[:8])
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	mime := img.MimeType
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// handleForkSession creates a new session that is an exact copy of the parent
// at this instant (same history, model, stats) with a new ID and fresh timestamps.
func (s *Server) handleForkSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Refuse if the parent session is busy — JSONL may be mid-write.
	s.mu.RLock()
	proxy, active := s.sessions[id]
	s.mu.RUnlock()
	if active && proxy.session.IsBusy() {
		writeError(w, http.StatusConflict, "session is busy", nil)
		return
	}

	// The body is optional: {"channels": [...]} binds the fork to channels of
	// its own (it never inherits its parent's).
	// "profiles" REPLACES the profile names the fork inherits (absent or null
	// = keep the parent's, [] = none).
	var req struct {
		Channels   []types.InputChannel `json:"channels,omitempty"`
		Profiles   *[]string            `json:"profiles,omitempty"`
		Directives []string             `json:"directives,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	var opts []agent.SessionOption
	if req.Profiles != nil {
		opts = append(opts, agent.SessionWithEnabledProfiles(*req.Profiles...))
	}
	if len(req.Directives) > 0 {
		opts = append(opts, agent.SessionWithDirectives(req.Directives...))
	}
	if len(req.Channels) > 0 {
		opts = append(opts, agent.SessionWithChannels(req.Channels...))
	}
	sess, err := s.agent.ForkSession(id, opts...)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case channelErrStatus(err, 0) != 0:
			status = channelErrStatus(err, 0)
		case errors.Is(err, agent.ErrUnknownProfile):
			status = http.StatusBadRequest
		case errors.Is(err, agent.ErrNoModelAvailable):
			status = http.StatusConflict // nothing to run the copy on: connect a provider
		case strings.Contains(err.Error(), "not found"):
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}

	s.mu.Lock()
	s.sessions[sess.ID()] = newSessionProxy(sess, s.logger)
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, liveDetail(sess))
}

func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// The body is optional (historically this endpoint took none).
	var req resumeSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}

	sess, err := s.openSession(id, req.Profiles, req.Directives, req.Channels...)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case channelErrStatus(err, 0) != 0:
			status = channelErrStatus(err, 0)
		case errors.Is(err, agent.ErrUnknownProfile):
			status = http.StatusBadRequest
		case errors.Is(err, agent.ErrNoModelAvailable):
			// Neither the session's model nor any fallback is usable: not a
			// server fault — the operator must connect a provider.
			status = http.StatusConflict
		case strings.Contains(err.Error(), "not found"):
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	// Meta() is the lock-free public accessor — safe even if the session is
	// mid-turn (see its doc comment).
	writeJSON(w, http.StatusOK, liveDetail(sess))
}

// openSession resumes session id in this server — idempotent and atomic, so
// a session has at most ONE proxy here no matter how many clients open it
// concurrently (openMu). profiles, when non-nil, REPLACE the session's
// persisted binding (an empty one removes them all); the agent applies it —
// on a fresh open and on a session that is already active (at its next turn
// boundary, never mid-turn).
func (s *Server) openSession(id string, profileNames *[]string, directives []string, channels ...types.InputChannel) (*agent.Session, error) {
	s.openMu.Lock()
	defer s.openMu.Unlock()

	var opts []agent.SessionOption
	if profileNames != nil {
		opts = append(opts, agent.SessionWithEnabledProfiles(*profileNames...))
	}
	if len(directives) > 0 {
		opts = append(opts, agent.SessionWithDirectives(directives...))
	}
	if len(channels) > 0 {
		opts = append(opts, agent.SessionWithChannels(channels...))
	}
	sess, err := s.agent.ResumeSession(id, opts...)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if _, ok := s.sessions[id]; !ok {
		s.sessions[id] = newSessionProxy(sess, s.logger)
	}
	s.mu.Unlock()
	return sess, nil
}

// sessionDetailDTO wraps store.SessionMeta with fields that live on the runtime
// Session (or the Agent as a fallback), not in the persisted meta — currently
// just MaxIterations, which the TUI footer uses for a "(turn/max_iterations)"
// indicator. Also embedded in sessionInfoDTO for the /info endpoint.
type sessionDetailDTO struct {
	store.SessionMeta
	MaxIterations int `json:"max_iterations"`
	// ModelFallback is present when the session was reopened on a different
	// model than it was saved with, because the saved model's provider was
	// unavailable (disconnected, credentials removed, model retired). `model`
	// is then the model it runs on now (and is saved with from now on); this
	// says what it was and why it changed. Omitted in the normal case.
	ModelFallback *agent.ModelFallback `json:"model_fallback,omitempty"`
	// MissingProfiles are bound profiles that are not registered right now:
	// skipped (the session works without them), their names kept. Omitted
	// when none.
	MissingProfiles []string `json:"missing_profiles,omitempty"`
	// Channels are the external conversations the live session listens to.
	// Omitted when none (and for a persisted session: channels are never
	// stored).
	Channels []agent.ChannelInfo `json:"channels,omitempty"`
}

// liveDetail is the detail DTO of an active session.
func liveDetail(sess *agent.Session) sessionDetailDTO {
	return sessionDetailDTO{SessionMeta: sess.Meta(), MaxIterations: sess.MaxIterations(), ModelFallback: sess.ModelFallback(), Channels: sess.Channels(), MissingProfiles: sess.MissingProfiles()}
}

// channelErrStatus maps a session-open failure caused by a channel to its HTTP
// status (409 taken, 400 unavailable/invalid), else def.
func channelErrStatus(err error, def int) int {
	switch {
	case errors.Is(err, agent.ErrChannelTaken):
		return http.StatusConflict
	case errors.Is(err, agent.ErrChannelUnavailable), errors.Is(err, agent.ErrChannelInvalid):
		return http.StatusBadRequest
	}
	return def
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Check in-memory first (active sessions). Meta() is the lock-free
	// public accessor, so this read-only lookup never blocks on a running
	// turn — see handleSessionInfo's identical comment and Meta()'s own doc
	// comment for the full rationale.
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if ok {
		writeJSON(w, http.StatusOK, liveDetail(proxy.session))
		return
	}

	// Fallback: the stored session (from a previous run). There's no live
	// *Session to ask, so MaxIterations comes from the agent's configured
	// default (AgentOptions.MaxIterations, already clamped by New()).
	meta, err := s.admin.Sessions.Get(id)
	if err != nil {
		if errors.Is(err, admin.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found", nil)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionDetailDTO{
		SessionMeta:   meta,
		MaxIterations: s.agent.Options().MaxIterations,
	})
}

type promptRequest struct {
	Text   string            `json:"text"`
	Images []types.ImageData `json:"images,omitempty"`
	// Origin tags where the prompt came from — any short string: "user"
	// (default), a channel conversation ("slack:C1"), a frontend's name
	// ("acp"), or a caller's own. Echoed on received_prompt/follow_up_start.
	Origin string `json:"origin,omitempty"`
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", nil)
		return
	}

	var req promptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	if req.Text == "" && len(req.Images) == 0 {
		writeError(w, http.StatusBadRequest, "text or images is required", nil)
		return
	}

	// Validate vision support if images are provided
	if len(req.Images) > 0 {
		meta := proxy.session.ModelMeta()
		if meta == nil || !meta.Vision {
			writeError(w, http.StatusBadRequest, "current model does not support images", nil)
			return
		}
	}

	opts := []agent.PromptOption{}
	if len(req.Images) > 0 {
		opts = append(opts, agent.PromptWithImages(req.Images...))
	}
	opts = append(opts, agent.PromptWithOrigin(req.Origin))
	ps := proxy.session.Prompt(context.Background(), req.Text, opts...)

	status := "started"
	if ps == types.PromptQueued {
		status = "queued"
	}
	writeStatus(w, http.StatusAccepted, status, "")
}

// handleAsk sends a prompt and blocks until the agent's turn completes,
// returning the final assistant text (or an error). This is the synchronous
// counterpart to /prompt (which is fire-and-forget). Useful for SDK callers
// and CLI one-shot commands that want a single request/response.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", nil)
		return
	}

	var req promptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	if req.Text == "" && len(req.Images) == 0 {
		writeError(w, http.StatusBadRequest, "text or images is required", nil)
		return
	}

	// Validate vision support if images are provided.
	if len(req.Images) > 0 {
		meta := proxy.session.ModelMeta()
		if meta == nil || !meta.Vision {
			writeError(w, http.StatusBadRequest, "current model does not support images", nil)
			return
		}
	}

	opts := []agent.PromptOption{agent.PromptWithOrigin(req.Origin)}
	if len(req.Images) > 0 {
		opts = append(opts, agent.PromptWithImages(req.Images...))
	}

	// PromptAndWait blocks until the turn finishes — the HTTP client controls
	// the timeout via its own request context.
	text, err := proxy.session.PromptAndWait(r.Context(), req.Text, opts...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// handleEvents streams agent events as SSE.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "session not found", nil)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported", nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Register the client BEFORE the response headers go out. The headers are what a client waits
	// for (client.StreamEvents returns when it has them), so a client that has "connected" must
	// already be receiving: registering after the flush left a window — a few microseconds, seen in
	// 2 of 4000 connections — in which an event emitted right after StreamEvents returned (the first
	// message of `harness "task"`, which subscribes and then sends at once) reached no one and its
	// echo was lost. Events that arrive before the flush wait in ch (its buffer) and are written
	// right after the headers, in order.
	watcher, release := proxy.watch(sseClientBufferSize)
	defer release()

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Stream events until the client disconnects or the session closes.
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-watcher.Events():
			if !ok {
				return
			}
			if line := formatEvent(e); line != nil {
				w.Write(line) //nolint:errcheck
				flusher.Flush()
			}
		}
	}
}

// sessionInfoDTO is the response shape for GET /api/sessions/{id}/info.
// It consolidates everything the TUI footer, Telegram /info, and any future
// frontend needs into one round-trip: server version, full session meta,
// live runtime state (busy, queue depth), and environment counts (MCPs,
// schedules). Read-only — no side effects.
type sessionInfoDTO struct {
	// Harness version string (e.g. "v0.73.40") from the running binary.
	Version string `json:"version"`
	// Full session metadata + max_iterations (same as GET /api/sessions/{id}).
	Session sessionDetailDTO `json:"session"`
	// Runtime state — only meaningful while the session is active.
	Busy       bool `json:"busy"`
	QueueDepth int  `json:"queue_depth"`
	// Environment counts for this session.
	MCPConnected  int `json:"mcp_connected"`
	ScheduleCount int `json:"schedule_count"`
	// The system the session runs on, and its terminal when the agent runs
	// in one (agent.AgentOptions.Terminal) — the same as the SessionInfo tool.
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Terminal string `json:"terminal,omitempty"`
}

// handleSessionInfo handles GET /api/sessions/{id}/info. The session must be
// active (in s.sessions). Returns 400 if it isn't — the endpoint is designed
// for live interactive use, not querying historical sessions.
func (s *Server) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}

	// Snapshot() is built only from lock-free accessors: this read-only
	// endpoint never blocks on a running turn (it used to, for the whole turn,
	// when it took the session lock). The same snapshot backs a channel's
	// agent.SessionView, so /info reads the same everywhere.
	snap := proxy.session.Snapshot()
	writeJSON(w, http.StatusOK, sessionInfoDTO{
		Version:       snap.Version,
		Session:       liveDetail(proxy.session),
		Busy:          snap.Busy,
		QueueDepth:    snap.QueueDepth,
		MCPConnected:  snap.MCPConnected,
		ScheduleCount: snap.ScheduleCount,
		OS:            snap.OS,
		Arch:          snap.Arch,
		Terminal:      snap.Terminal,
	})
}

// handleSessionContext handles GET /api/sessions/{id}/context. Returns a
// token-usage breakdown of the session's context window, estimated per
// component. Only valid for active sessions. ContextBreakdown() itself is
// lock-free (see its own doc comment) — this endpoint never blocks on an
// in-flight turn, same as handleSessionInfo/handleGetSession.
func (s *Server) handleSessionContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}
	writeJSON(w, http.StatusOK, proxy.session.ContextBreakdown())
}

// handleSessionSystemPrompt handles GET /api/sessions/{id}/sysprompt: the
// full system prompt the session sends to the provider — base prompt, skills,
// project context, memory block and every directive (agent-wide plus the
// session's own, see agent.SessionWithDirectives) — as raw markdown
// (text/markdown), not JSON. Built when the session was created/resumed and
// immutable afterwards, so Session.SystemPrompt() is lock-free: safe against
// a busy session, like /info and /context. Only valid for active sessions.
func (s *Server) handleSessionSystemPrompt(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, proxy.session.SystemPrompt()) //nolint:errcheck
}

// handleListTools handles GET /api/sessions/{id}/tools. Returns the full
// set of tool definitions (name, description, input_schema) currently
// registered for the session — the exact set sent to the provider on the
// next turn, including any MCP tools and SDK-supplied AgentOptions.Tools
// alongside the built-ins. Pure introspection: this is NOT tied to any
// frontend and doesn't affect what the model can call — it only tells a
// caller what's available. Only valid for active sessions.
func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}
	writeJSON(w, http.StatusOK, proxy.session.Tools())
}

// --- Commands ---

type commandDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Params      []paramDef `json:"params"`
}

type paramDef struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
}

var commands = []commandDef{
	{
		Name:        "rename",
		Description: "Rename the session",
		Params: []paramDef{
			{Name: "name", Type: "string", Required: true},
		},
	},
	{
		Name:        "thinking",
		Description: "Set the thinking level",
		Params: []paramDef{
			{Name: "level", Type: "string", Required: true, Values: []string{"off", "low", "medium", "high", "xhigh", "max"}},
		},
	},
	{
		Name:        "model",
		Description: "Switch to a different model",
		Params: []paramDef{
			{Name: "model", Type: "string", Required: true},
		},
	},
	{
		Name:        "max-iter",
		Description: "Override the max ReAct iterations allowed per turn for this session (1-1000)",
		Params: []paramDef{
			{Name: "value", Type: "integer", Required: true},
		},
	},
	{
		Name:        "compact",
		Description: "Compact the conversation via LLM summary",
		Params:      []paramDef{},
	},
	{
		Name:        "reset",
		Description: "Wipe the session history and stats, starting fresh (preserves model, thinking, and name)",
		Params:      []paramDef{},
	},
	{
		Name:        "export",
		Description: "Export the session (its whole conversation) to ~/.harness/exports/<name>-<id>.export.jsonl",
		Params:      []paramDef{},
	},
	{
		Name:        "import",
		Description: "Import a session export (.export.jsonl) as a NEW session; the session the command runs on is untouched",
		Params: []paramDef{
			{Name: "path", Type: "string", Required: true},
		},
	},
}

func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}

	list := make([]commandDef, len(commands))
	copy(list, commands)

	// Populate model values dynamically
	for i, cmd := range list {
		if cmd.Name == "model" {
			for j, p := range list[i].Params {
				if p.Name == "model" {
					var vals []string
					for _, prov := range providers.All() {
						if !prov.IsActive() {
							continue
						}
						for _, m := range prov.Models() {
							vals = append(vals, prov.Name()+"/"+m.ID)
						}
					}
					list[i].Params[j].Values = vals
				}
			}
		}
	}

	for _, sk := range proxy.session.Skills() {
		list = append(list, commandDef{
			Name:        "skill:" + sk.Name,
			Description: sk.Description,
			Params: []paramDef{
				{Name: "prompt", Type: "string", Required: false},
			},
		})
	}

	writeJSON(w, http.StatusOK, list)
}

type execCommandRequest struct {
	Command string         `json:"command"`
	Params  map[string]any `json:"params"`
}

func (s *Server) handleExecCommand(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}

	var req execCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}

	switch req.Command {
	case "rename":
		name, _ := req.Params["name"].(string)
		if name == "" {
			writeError(w, http.StatusBadRequest, "param 'name' is required", nil)
			return
		}
		if err := proxy.session.Rename(name); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeStatus(w, http.StatusOK, "ok", "")

	case "thinking":
		level, _ := req.Params["level"].(string)
		if level == "" {
			writeError(w, http.StatusBadRequest, "param 'level' is required", nil)
			return
		}
		// This command changes ONLY the live session — it deliberately does
		// NOT write the global default (settings.json). Changing a session's
		// thinking shouldn't silently re-configure every FUTURE session (and,
		// cross-process, every other running instance); use
		// `harness settings set thinking` for that. SwitchThinking validates
		// the level and returns ErrInvalidThinkingLevel (mapped to 422) for a
		// bad value, before any session state is touched.
		if err := proxy.session.SwitchThinking(level); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, config.ErrInvalidThinkingLevel) {
				status = http.StatusUnprocessableEntity
			}
			writeErr(w, status, err)
			return
		}
		writeStatus(w, http.StatusOK, "ok", "")

	case "model":
		model, _ := req.Params["model"].(string)
		if model == "" {
			writeError(w, http.StatusBadRequest, "param 'model' is required", nil)
			return
		}
		// Changes ONLY the live session — like the thinking command above, it
		// does NOT write the global default. SwitchModel validates the model
		// (provider active + model exists) and returns an error if not, so an
		// invalid model never mutates session state.
		if err := proxy.session.SwitchModel(context.Background(), model); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeStatus(w, http.StatusOK, "ok", "")

	case "max-iter":
		// Params travel over JSON as map[string]any — the TUI/CLI always send
		// it as a string (client.ExecCommand's params are built from typed
		// user input, same as "model"/"thinking"), but a direct API caller
		// could reasonably send a JSON number instead, so accept both rather
		// than requiring one specific wire representation.
		var n int
		switch v := req.Params["value"].(type) {
		case string:
			parsed, err := strconv.Atoi(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "param 'value' must be an integer", nil)
				return
			}
			n = parsed
		case float64: // encoding/json decodes a bare JSON number into float64
			n = int(v)
		default:
			writeError(w, http.StatusBadRequest, "param 'value' is required", nil)
			return
		}
		// SetMaxIterations validates the [1, 1000] bound itself — same
		// pattern as SwitchThinking above: invalid input never touches
		// session state, mapped to 422 like SwitchThinking's
		// ErrInvalidThinkingLevel.
		if err := proxy.session.SetMaxIterations(n); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeStatus(w, http.StatusOK, "ok", "")

	case "compact":
		// Refuse to compact mid-turn (it would corrupt the active conversation).
		// The response shape is consistent — always {"status": ...} — with the HTTP
		// code signaling the outcome (409 for the busy conflict), so clients read
		// status rather than sniffing an error string.
		if proxy.session.IsBusy() {
			writeError(w, http.StatusConflict, "session is busy", nil)
			return
		}
		go proxy.session.Compact(context.Background()) //nolint
		writeStatus(w, http.StatusAccepted, "started", "")

	case "reset":
		if proxy.session.IsBusy() {
			writeError(w, http.StatusConflict, "session is busy", nil)
			return
		}
		if err := proxy.session.Reset(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeStatus(w, http.StatusOK, "ok", "")

	case "export":
		msg, err := s.commandExport(proxy.session)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeStatus(w, http.StatusOK, "ok", msg)

	case "import":
		path, _ := req.Params["path"].(string)
		if path == "" {
			writeError(w, http.StatusBadRequest, "param 'path' is required", nil)
			return
		}
		id, err := s.commandImport(proxy.session, path)
		if err != nil {
			s.writeImportErr(w, err, 0)
			return
		}
		// The message IS the new session's id: the caller switches to it.
		writeStatus(w, http.StatusCreated, "ok", id)

	default:
		// Check if it's a skill command: skill:<name>
		if strings.HasPrefix(req.Command, "skill:") {
			skillName := strings.TrimPrefix(req.Command, "skill:")
			content, dir, err := proxy.session.ReadSkill(skillName)
			if err != nil {
				writeError(w, http.StatusNotFound, "skill not found: "+skillName, nil)
				return
			}
			// Build prompt: skill location note + content + optional user prompt. The
			// location lets the model resolve relative paths the skill references.
			userPrompt, _ := req.Params["prompt"].(string)
			prompt := fmt.Sprintf("This skill is located at %s\nAny relative paths it references are relative to this directory.\n\n%s", dir, content)
			if userPrompt != "" {
				prompt += "\n\n---\n\n" + userPrompt
			}
			// PromptWithDisplayText keeps the skill's full body (can be
			// large — SKILL.md content, not just a short instruction) OUT
			// of what a frontend echoes back to the user: it shows
			// "Skill:<name> <their own text>" (mirroring the literal
			// "skill:<name>" command syntax, no space before the name)
			// instead of the entire injected prompt scrolling past. The
			// model itself still receives the full prompt unchanged.
			display := "Skill:" + skillName
			if userPrompt != "" {
				display += " " + userPrompt
			}
			ps := proxy.session.Prompt(context.Background(), prompt, agent.PromptWithDisplayText(display))
			status := "started"
			if ps == types.PromptQueued {
				status = "queued"
			}
			writeStatus(w, http.StatusAccepted, status, "")
			return
		}
		writeError(w, http.StatusBadRequest, "unknown command: "+req.Command, nil)
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes a standard error response: {"error": {"message": ...,
// "details": {...}}}. details is optional (omitted when nil). All error
// responses go through here so the shape is consistent across endpoints.
func writeError(w http.ResponseWriter, status int, message string, details map[string]any) {
	err := map[string]any{"message": message}
	if len(details) > 0 {
		err["details"] = details
	}
	writeJSON(w, status, map[string]any{"error": err})
}

// writeErr writes an error response from a Go error, lifting a provider
// ProviderAPIError's structured details into the response when present.
func writeErr(w http.ResponseWriter, status int, e error) {
	var apiErr *types.ProviderAPIError
	if errors.As(e, &apiErr) {
		writeError(w, status, apiErr.Message, apiErr.Details)
		return
	}
	writeError(w, status, e.Error(), nil)
}

// writeStatus writes a 2XX action-confirmation response in the standard shape:
// {"status": {"code": ..., "message": ...}}. message is optional (omitted when
// empty). Only for successful (2XX) responses; errors use writeError/writeErr.
func writeStatus(w http.ResponseWriter, httpCode int, code string, message string) {
	s := map[string]any{"code": code}
	if message != "" {
		s["message"] = message
	}
	writeJSON(w, httpCode, map[string]any{"status": s})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Harness-Sec")
		// Without this a browser client can't read GET /messages' paging headers.
		w.Header().Set("Access-Control-Expose-Headers", "X-Messages-From, X-Messages-Total")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(docsHTML)) //nolint:errcheck
}

func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	addr := s.addr
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(openAPISpecJSON(addr))) //nolint:errcheck
}
