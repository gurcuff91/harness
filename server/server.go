package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
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
// (SessionProxy.broadcast's target, and handleEvents' consumer). A turn with
// thinking:high and a long response can emit thousands of small delta events
// (one per streamed token/fragment); if the consumer (the render loop on the
// other end of the connection) falls behind a burst, this is how much
// slack it gets before a delta is dropped (control events like turn_end/stop
// get a bounded blocking retry instead — see isControlEvent in proxy.go).
// 4096 is generous headroom for that burst (each event is at most a few
// hundred bytes, so worst case this is a few MB — negligible) without being
// unbounded. Matches tuiClientEventBufferSize on the TUI's consuming side
// (internal/tui/client.go) — the two ends of the same pipe should
// have comparable slack, not one starving the other.
const sseClientBufferSize = 4096

// Server is the HTTP transport for the agent harness.
type Server struct {
	agent        *agent.Agent
	logger       logx.Logger // never nil — defaults to logx.NilLogger{}
	transport    string      // name of the calling transport
	addr         string      // resolved listen address (set in Serve)
	instanceName string      // unique instance name (MK11-themed, set in Serve)
	mu           sync.RWMutex
	sessions     map[string]*SessionProxy

	// Graceful shutdown plumbing (set in Serve).
	httpSrv   *http.Server
	listener  net.Listener
	closeOnce sync.Once
	closeErr  error
}

// ServerOptions configures the HTTP server.
type ServerOptions struct {
	// Logger receives request/lifecycle log lines. Defaults to
	// logx.NilLogger{} (silent) when left nil — the same default Run's
	// WithLogger option falls back to. Pass logx.NilLogger{} explicitly (as
	// every transport's own in-process server does) when something ELSE is
	// already logging and this server must stay silent.
	Logger    logx.Logger
	Transport string // name of the calling transport (e.g. "tui", "telegram", "slack"); empty defaults to "server"
}

// NewServer creates an HTTP server wrapping the agent.
func NewServer(a *agent.Agent, opts ServerOptions) *Server {
	transport := opts.Transport
	if transport == "" {
		transport = "server"
	}
	logger := opts.Logger
	if logger == nil {
		logger = logx.NilLogger{}
	}
	return &Server{
		agent:     a,
		logger:    logger,
		transport: transport,
		sessions:  make(map[string]*SessionProxy),
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

	// Routes
	r.Get("/api/server", s.handleServerInfo)
	r.Get("/api/instances", s.handleListInstances)
	r.Get("/api/docs", s.handleDocs)
	r.Get("/api/docs/openapi.json", s.handleOpenAPISpec)
	r.Get("/api/settings", s.handleSettings)
	r.Patch("/api/settings", s.handlePatchSettings)
	r.Get("/api/settings/providers", s.handleListProviderConfigs)
	r.Put("/api/settings/providers/{name}", s.handlePutProviderConfig)
	r.Delete("/api/settings/providers/{name}", s.handleDeleteProviderConfig)
	r.Get("/api/settings/mcp", s.handleListMCPServers)
	r.Put("/api/settings/mcp/{name}", s.handlePutMCPServer)
	r.Delete("/api/settings/mcp/{name}", s.handleDeleteMCPServer)
	r.Get("/api/mcp/status", s.handleMCPStatus)
	r.Get("/api/memories", s.handleListMemories)
	r.Get("/api/schedules", s.handleListSchedules)
	r.Get("/api/providers", s.handleProviders)
	r.Post("/api/providers/{name}/connect", s.handleConnectProvider)
	r.Post("/api/providers/{name}/disconnect", s.handleDisconnectProvider)
	r.Get("/api/models", s.handleModels)
	r.Get("/api/sessions", s.handleListSessions)
	r.Post("/api/sessions", s.handleCreateSession)
	r.Get("/api/sessions/{id}", s.handleGetSession)
	r.Delete("/api/sessions/{id}", s.handleDeleteSession)
	r.Post("/api/sessions/{id}/close", s.handleCloseSession)
	r.Post("/api/sessions/{id}/resume", s.handleResumeSession)
	r.Post("/api/sessions/{id}/fork", s.handleForkSession)
	r.Post("/api/sessions/{id}/prompt", s.handlePrompt)
	r.Post("/api/sessions/{id}/ask", s.handleAsk)
	r.Get("/api/sessions/{id}/events", s.handleEvents)
	r.Get("/api/sessions/{id}/commands", s.handleListCommands)
	r.Post("/api/sessions/{id}/commands", s.handleExecCommand)
	r.Get("/api/sessions/{id}/messages", s.handleGetMessages)
	r.Post("/api/sessions/{id}/stop", s.handleStopSession)
	r.Get("/api/sessions/{id}/info", s.handleSessionInfo)
	r.Get("/api/sessions/{id}/context", s.handleSessionContext)

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

// Serve runs the HTTP transport on an already-open listener. This is the single
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

	// Register this instance in ~/.harness/instances.json.
	name, err := RegisterInstance(InstanceInfo{
		Version:   staticServerVersion,
		Transport: s.transport,
		URL:       "http://" + addr,
		CWD:       staticServerCWD,
		PID:       staticServerPID,
		StartedAt: staticServerStartedAt,
	})
	if err != nil {
		s.logger.Warn("server", "register_instance", "error", err.Error())
	} else {
		s.mu.Lock()
		s.instanceName = name
		s.mu.Unlock()
		s.logger.Info("server", "instance", "name", name)
	}

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

		// 2. Close the agent — MCP, memory, scheduler, store.
		s.closeErr = s.agent.Close()

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
		instanceName := s.instanceName
		s.mu.RUnlock()
		if httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := httpSrv.Shutdown(ctx); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}

		// 4. Unregister this instance from ~/.harness/instances.json.
		if instanceName != "" {
			UnregisterInstance(instanceName)
		}
	})
	return s.closeErr
}

// --- Handlers ---

type createSessionRequest struct {
	Model string `json:"model"`
	CWD   string `json:"cwd"`
	Name  string `json:"name,omitempty"` // optional initial name; default: "New Session <date>"
}

// serverInfo is returned by GET /api/server.
type serverInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Instance  string `json:"instance"`
	Transport string `json:"transport"`
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
	addr, instanceName := s.addr, s.instanceName
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, serverInfo{
		Name:      staticServerName,
		Version:   staticServerVersion,
		Instance:  instanceName,
		Transport: s.transport,
		URL:       "http://" + addr,
		CWD:       staticServerCWD,
		PID:       staticServerPID,
		StartedAt: staticServerStartedAt,
	})
}

// handleListInstances returns all registered server instances from
// ~/.harness/instances.json (dead PIDs are pruned on load).
func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	instances, err := ListInstances()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if instances == nil {
		instances = map[string]InstanceInfo{}
	}
	writeJSON(w, http.StatusOK, instances)
}

// SettingsDTO is the API representation of harness settings. Its JSON tags are
// the single source of truth for the settings contract and match the on-disk
// field names (see config.settingsData) — one vocabulary end to end.
type SettingsDTO struct {
	ActiveModel   string `json:"active_model"`
	ThinkingLevel string `json:"thinking_level"`
}

// handleSettings returns current settings with defaults.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	sm := config.GetSettingsManager()
	thinking := sm.ThinkingLevel()
	if thinking == "" {
		thinking = "off"
	}
	writeJSON(w, http.StatusOK, SettingsDTO{
		ActiveModel:   sm.ActiveModel(),
		ThinkingLevel: thinking,
	})
}

// handlePatchSettings partially updates core settings. Only the fields present
// in the body are changed — this persists the global DEFAULT and does NOT touch
// any live session (use the session's model/thinking command for that).
func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActiveModel   *string `json:"active_model"`
		ThinkingLevel *string `json:"thinking_level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	sm := config.GetSettingsManager()
	if body.ActiveModel != nil {
		if err := sm.SetActiveModel(*body.ActiveModel); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if body.ThinkingLevel != nil {
		if err := sm.SetThinkingLevel(*body.ThinkingLevel); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, config.ErrInvalidThinkingLevel) {
				status = http.StatusUnprocessableEntity
			}
			writeErr(w, status, err)
			return
		}
	}
	thinking := sm.ThinkingLevel()
	if thinking == "" {
		thinking = "off"
	}
	writeJSON(w, http.StatusOK, SettingsDTO{ActiveModel: sm.ActiveModel(), ThinkingLevel: thinking})
}

// ── Provider configs (settings collection) ────────────────────────────────

// handleListProviderConfigs returns the whole provider-config collection.
func (s *Server) handleListProviderConfigs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, config.GetSettingsManager().Providers())
}

// handlePutProviderConfig stores (or replaces) one provider's config. The name
// is in the URL; the ProviderConfig is the body. Pass-through: any validation
// lives in the SettingsManager's setter, not here.
func (s *Server) handlePutProviderConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var cfg config.ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	if err := config.GetSettingsManager().SetProvider(name, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleDeleteProviderConfig removes one provider's config, 404 if absent.
func (s *Server) handleDeleteProviderConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sm := config.GetSettingsManager()
	if _, ok := sm.Provider(name); !ok {
		writeError(w, http.StatusNotFound, "provider config not found: "+name, nil)
		return
	}
	if err := sm.DeleteProvider(name); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeStatus(w, http.StatusOK, "deleted", "")
}

// ── MCP servers (settings collection) ───────────────────────────────────

// handleListMCPServers returns the whole MCP-server collection.
func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, config.GetSettingsManager().MCPServers())
}

// handlePutMCPServer stores (or replaces) one MCP server. Name in URL, MCPServer
// in the body. Pass-through, same as provider configs.
func (s *Server) handlePutMCPServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var srv config.MCPServer
	if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	if err := config.GetSettingsManager().SetMCPServer(name, srv); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, config.ErrInvalidMCPServer) {
			status = http.StatusUnprocessableEntity // 422: well-formed JSON, invalid content
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

// handleListSchedules serves the read-only list of cron-scheduled prompts, in
// the same shape as the ScheduleList tool (slug, cron, prompt, runs, last_run).
// Writes/deletes are not exposed — only the agent mutates schedules via its tools.
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	st := s.agent.Schedules()
	if st == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	// Optional ?owner=<session_id> filters to the schedules that will actually
	// fire into that session — the honest count for a single session's view
	// (a schedule only ever runs in its owner session). Omit owner for all
	// (the operator view, e.g. `harness schedules`).
	owner := r.URL.Query().Get("owner")
	type entry struct {
		Slug    string `json:"slug"`
		Cron    string `json:"cron"`
		Prompt  string `json:"prompt"`
		Runs    int    `json:"runs"`
		LastRun int64  `json:"last_run,omitempty"`
	}
	out := []entry{}
	for _, sc := range st.List() {
		if owner != "" && sc.Owner != owner {
			continue
		}
		out = append(out, entry{sc.Slug, sc.Cron, sc.Prompt, sc.Runs, sc.LastRun})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListMemories serves read-only memory queries. Memories are partitioned
// by working directory; the cwd query param is an OPTIONAL filter (omit it for a
// global view across all projects). Writes/deletes are intentionally NOT exposed
// — only the agent mutates memory, via its tools. All params are optional:
//
//	cwd, query, include_content (default true), skip (default 0), limit (default 10)
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	mem := s.agent.Memory()
	if mem == nil {
		writeJSON(w, http.StatusOK, map[string]any{"total": 0, "returned": 0, "skip": 0, "limit": 0, "results": []any{}})
		return
	}
	q := r.URL.Query()
	cwd := q.Get("cwd")
	query := q.Get("query")
	includeContent := q.Get("include_content") != "false" // default true
	skip := atoiDefault(q.Get("skip"), 0)
	limit := atoiDefault(q.Get("limit"), 10)

	res, err := mem.Search(cwd, query, includeContent, skip, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// atoiDefault parses s as an int, returning def when empty or invalid.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
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

// handleDeleteMCPServer removes one MCP server, 404 if absent.
func (s *Server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sm := config.GetSettingsManager()
	if _, ok := sm.MCPServer(name); !ok {
		writeError(w, http.StatusNotFound, "mcp server not found: "+name, nil)
		return
	}
	if err := sm.DeleteMCPServer(name); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeStatus(w, http.StatusOK, "deleted", "")
}

// providerInfo is the API representation of a provider.
type providerInfo struct {
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	Active         bool   `json:"active"`
	Activation     string `json:"activation"`
	IsSubscription bool   `json:"is_subscription"`
	// CredentialType is the authoritative kind of credential the provider needs:
	// "none" (auto-detected, e.g. ollama), "api_key" (secret), or "oauth". The
	// CLI uses this to decide whether to prompt for a secret at connect time.
	CredentialType string `json:"credential_type"`
	ModelCount     int    `json:"model_count"`
}

// handleProviders returns all registered providers.
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	providers.EnsureRegistry()
	var list []providerInfo
	for _, p := range providers.All {
		models := p.Models()
		if p.IsActive() && len(models) == 0 {
			// Lazy-fetch models for active providers
			models, _ = p.FetchModels()
		}
		act := string(p.ActivationSource())
		if act == "none" {
			act = "inactive"
		} else if act == "envvar" {
			act = "env"
		}
		list = append(list, providerInfo{
			Name:           p.Name(),
			DisplayName:    p.DisplayName(),
			Description:    p.Description(),
			Active:         p.IsActive(),
			Activation:     act,
			IsSubscription: p.CredentialType() == types.CredTypeOAuth,
			CredentialType: string(p.CredentialType()),
			ModelCount:     len(models),
		})
	}
	if list == nil {
		list = []providerInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

// modelInfo groups a model with its provider.
type modelInfo struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	IsSubscription bool   `json:"is_subscription"`
	types.ModelMeta
}

// connect/disconnect request types
type connectRequest struct {
	APIKey       string `json:"api_key,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
}

func (s *Server) handleConnectProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	providers.EnsureRegistry()

	var target providers.Provider
	for _, p := range providers.All {
		if p.Name() == name {
			target = p
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "provider not found: "+name, nil)
		return
	}

	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}

	creds := types.Credentials{
		Type:         target.CredentialType(),
		APIKey:       req.APIKey,
		AccessToken:  req.AccessToken,
		RefreshToken: req.RefreshToken,
		ExpiresAt:    req.ExpiresAt,
	}

	if err := target.Connect(creds); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	writeStatus(w, http.StatusOK, "connected", fmt.Sprintf("%d models", len(target.Models())))
}

func (s *Server) handleDisconnectProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	providers.EnsureRegistry()

	var target providers.Provider
	for _, p := range providers.All {
		if p.Name() == name {
			target = p
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "provider not found: "+name, nil)
		return
	}

	if err := target.Disconnect(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	writeStatus(w, http.StatusOK, "disconnected", "")
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	providers.EnsureRegistry()
	var list []modelInfo
	for _, p := range providers.All {
		if !p.IsActive() {
			continue
		}
		models := p.Models()
		if len(models) == 0 {
			models, _ = p.FetchModels()
		}
		for _, m := range models {
			list = append(list, modelInfo{
				Provider:       p.Name(),
				Model:          p.Name() + "/" + m.ID,
				IsSubscription: p.CredentialType() == types.CredTypeOAuth,
				ModelMeta:      m,
			})
		}
	}
	if list == nil {
		list = []modelInfo{}
	}
	writeJSON(w, http.StatusOK, list)
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

	sess, err := s.agent.NewSession(req.CWD, req.Model)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Apply optional initial name (transport-supplied, e.g. "Slack 2026-07-27").
	if req.Name != "" {
		_ = sess.Rename(req.Name)
	}

	// Apply thinking level from settings
	sm := config.GetSettingsManager()
	if level := sm.ThinkingLevel(); level != "" && level != "off" {
		_ = sess.SwitchThinking(level)
	}

	proxy := newSessionProxy(sess, s.logger)

	s.mu.Lock()
	s.sessions[sess.ID()] = proxy
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, sessionDetailDTO{SessionMeta: sess.Meta(), MaxIterations: sess.MaxIterations()})
}

// handleListSessions returns all sessions, optionally filtered by ?cwd=
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	cwd := r.URL.Query().Get("cwd")
	var sessions []store.SessionMeta
	var err error
	if cwd != "" {
		sessions, err = s.agent.ListSessions(cwd)
	} else {
		sessions, err = s.agent.ListAllSessions()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if sessions == nil {
		sessions = []store.SessionMeta{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

// handleDeleteSession deletes a session permanently.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Close and remove from in-memory if active
	s.mu.Lock()
	proxy, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
		proxy.close()
	}
	s.mu.Unlock()

	if err := s.agent.DeleteSession(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "session is not active", nil)
		return
	}
	messages := proxy.session.AllMessages()
	writeJSON(w, http.StatusOK, messages)
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

	sess, err := s.agent.ForkSession(id)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}

	s.mu.Lock()
	s.sessions[sess.ID()] = newSessionProxy(sess, s.logger)
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, sessionDetailDTO{SessionMeta: sess.Meta(), MaxIterations: sess.MaxIterations()})
}

func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Already active? Return the live session (idempotent — supports scheduler
	// auto-resume and transport reconnect without 409 errors).
	s.mu.RLock()
	if proxy, ok := s.sessions[id]; ok {
		s.mu.RUnlock()
		writeJSON(w, http.StatusOK, sessionDetailDTO{
			SessionMeta:   proxy.session.Meta(),
			MaxIterations: proxy.session.MaxIterations(),
		})
		return
	}
	s.mu.RUnlock()

	sess, err := s.agent.ResumeSession(id)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}

	proxy := newSessionProxy(sess, s.logger)
	s.mu.Lock()
	s.sessions[sess.ID()] = proxy
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, sessionDetailDTO{SessionMeta: sess.Meta(), MaxIterations: sess.MaxIterations()})
}

// sessionDetailDTO wraps store.SessionMeta with fields that live on the runtime
// Session (or the Agent as a fallback), not in the persisted meta — currently
// just MaxIterations, which the TUI footer uses for a "(turn/max_iterations)"
// indicator. Also embedded in sessionInfoDTO for the /info endpoint.
type sessionDetailDTO struct {
	store.SessionMeta
	MaxIterations int `json:"max_iterations"`
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Check in-memory first (active sessions)
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if ok {
		writeJSON(w, http.StatusOK, sessionDetailDTO{
			SessionMeta:   proxy.session.Meta(),
			MaxIterations: proxy.session.MaxIterations(),
		})
		return
	}

	// Fallback: check store (persisted sessions from previous runs). There's no
	// live *Session to ask, so MaxIterations comes from the agent's configured
	// default — every session it creates gets the same value.
	sessions, err := s.agent.ListAllSessions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, meta := range sessions {
		if meta.ID == id {
			writeJSON(w, http.StatusOK, sessionDetailDTO{
				SessionMeta:   meta,
				MaxIterations: s.agent.MaxIterations(),
			})
			return
		}
	}

	writeError(w, http.StatusNotFound, "session not found", nil)
}

type promptRequest struct {
	Text   string            `json:"text"`
	Images []types.ImageData `json:"images,omitempty"`
	Origin string            `json:"origin,omitempty"` // "user" (default) | "scheduled"
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
	if req.Origin == agent.OriginScheduled {
		opts = append(opts, agent.PromptWithOriginScheduled())
	}
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

	opts := []agent.PromptOption{agent.PromptWithOriginUser()}
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
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan []byte, sseClientBufferSize)
	proxy.addClient(ch)
	defer proxy.removeClient(ch)

	// Stream events until client disconnects
	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, string(line))
			flusher.Flush()
		}
	}
}

// sessionInfoDTO is the response shape for GET /api/sessions/{id}/info.
// It consolidates everything the TUI footer, Telegram /info, and any future
// transport needs into one round-trip: server version, full session meta,
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

	sess := proxy.session

	// MCP: count connected servers.
	mcpConnected := 0
	for _, st := range s.agent.MCPStatuses() {
		if st.Connected {
			mcpConnected++
		}
	}

	// Schedules: count only those owned by this session (they're the ones that
	// will actually fire into it — the honest count from this session's view).
	scheduleCount := 0
	if store := s.agent.Schedules(); store != nil {
		for _, sc := range store.List() {
			if sc.Owner == id {
				scheduleCount++
			}
		}
	}

	writeJSON(w, http.StatusOK, sessionInfoDTO{
		Version: version.Version,
		Session: sessionDetailDTO{
			SessionMeta:   sess.Meta(),
			MaxIterations: sess.MaxIterations(),
		},
		Busy:          sess.IsBusy(),
		QueueDepth:    sess.FollowUpCount(),
		MCPConnected:  mcpConnected,
		ScheduleCount: scheduleCount,
	})
}

// handleSessionContext handles GET /api/sessions/{id}/context. Returns a
// token-usage breakdown of the session's context window, estimated per
// component. Only valid for active sessions.
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
			{Name: "level", Type: "string", Required: true, Values: []string{"off", "low", "medium", "high", "xhigh"}},
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
		Name:        "compact",
		Description: "Compact the conversation via LLM summary",
		Params:      []paramDef{},
	},
	{
		Name:        "reset",
		Description: "Wipe the session history and stats, starting fresh (preserves model, thinking, and name)",
		Params:      []paramDef{},
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
					for _, prov := range providers.All {
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
		// Validate + persist FIRST. Only if the level is accepted do we apply it
		// to the live session, so an invalid value never mutates session state.
		if err := config.GetSettingsManager().SetThinkingLevel(level); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, config.ErrInvalidThinkingLevel) {
				status = http.StatusUnprocessableEntity
			}
			writeErr(w, status, err)
			return
		}
		if err := proxy.session.SwitchThinking(level); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeStatus(w, http.StatusOK, "ok", "")

	case "model":
		model, _ := req.Params["model"].(string)
		if model == "" {
			writeError(w, http.StatusBadRequest, "param 'model' is required", nil)
			return
		}
		if err := proxy.session.SwitchModel(context.Background(), model); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// Persist to settings
		_ = config.GetSettingsManager().SetActiveModel(model)
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
			prompt := fmt.Sprintf("This skill is located at %s\nAny relative paths it references are relative to this directory.\n\n%s", dir, content)
			if userPrompt, _ := req.Params["prompt"].(string); userPrompt != "" {
				prompt += "\n\n---\n\n" + userPrompt
			}
			ps := proxy.session.Prompt(context.Background(), prompt)
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

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
