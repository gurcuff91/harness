package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/mcp"
	"github.com/gurcuff91/harness/providers"
	"github.com/gurcuff91/harness/types"
)

// version is set via ldflags at build time.
var version = "dev"

// Server is the HTTP transport for the agent harness.
type Server struct {
	agent    *agent.Agent
	verbose  bool
	mu       sync.RWMutex
	sessions map[string]*SessionProxy
}

// ServerOptions configures the HTTP server.
type ServerOptions struct {
	Verbose bool // enable request logging (default: false)
}

// NewServer creates an HTTP server wrapping the agent.
func NewServer(a *agent.Agent, opts ServerOptions) *Server {
	return &Server{
		agent:    a,
		verbose:  opts.Verbose,
		sessions: make(map[string]*SessionProxy),
	}
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	r := chi.NewRouter()

	// Middleware
	if s.verbose {
		r.Use(middleware.Logger)
	}
	r.Use(corsMiddleware)

	// Routes
	r.Get("/api/server", s.handleServerInfo)
	r.Get("/api/settings", s.handleSettings)
	r.Patch("/api/settings", s.handlePatchSettings)
	r.Get("/api/settings/providers", s.handleListProviderConfigs)
	r.Put("/api/settings/providers/{name}", s.handlePutProviderConfig)
	r.Delete("/api/settings/providers/{name}", s.handleDeleteProviderConfig)
	r.Get("/api/settings/mcp", s.handleListMCPServers)
	r.Put("/api/settings/mcp/{name}", s.handlePutMCPServer)
	r.Delete("/api/settings/mcp/{name}", s.handleDeleteMCPServer)
	r.Get("/api/mcp/status", s.handleMCPStatus)
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
	r.Post("/api/sessions/{id}/prompt", s.handlePrompt)
	r.Get("/api/sessions/{id}/events", s.handleEvents)
	r.Get("/api/sessions/{id}/commands", s.handleListCommands)
	r.Post("/api/sessions/{id}/commands", s.handleExecCommand)
	r.Get("/api/sessions/{id}/messages", s.handleGetMessages)
	r.Post("/api/sessions/{id}/stop", s.handleStopSession)

	if s.verbose {
		log.Printf("⚔️  Harness HTTP transport listening on %s", addr)
	}
	return http.ListenAndServe(addr, r)
}

// --- Handlers ---

type createSessionRequest struct {
	Model string `json:"model"`
	CWD   string `json:"cwd"`
}

// serverInfo is returned by GET /api/server
var serverInfo = struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}{
	Name:      "harness",
	Version:   version,
	StartedAt: time.Now().UTC().Format(time.RFC3339),
}

func init() {
	var err error
	serverInfo.CWD, err = os.Getwd()
	if err != nil {
		serverInfo.CWD = "."
	}
	serverInfo.PID = os.Getpid()
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, serverInfo)
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	sm := config.GetSettingsManager()
	if body.ActiveModel != nil {
		if err := sm.SetActiveModel(*body.ActiveModel); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	if body.ThinkingLevel != nil {
		if err := sm.SetThinkingLevel(*body.ThinkingLevel); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, config.ErrInvalidThinkingLevel) {
				status = http.StatusUnprocessableEntity
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	if err := config.GetSettingsManager().SetProvider(name, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleDeleteProviderConfig removes one provider's config, 404 if absent.
func (s *Server) handleDeleteProviderConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sm := config.GetSettingsManager()
	if _, ok := sm.Provider(name); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider config not found: " + name})
		return
	}
	if err := sm.DeleteProvider(name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	if err := config.GetSettingsManager().SetMCPServer(name, srv); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, config.ErrInvalidMCPServer) {
			status = http.StatusUnprocessableEntity // 422: well-formed JSON, invalid content
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, srv)
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mcp server not found: " + name})
		return
	}
	if err := sm.DeleteMCPServer(name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not found: " + name})
		return
	}

	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "connected",
		"model_count": len(target.Models()),
	})
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not found: " + name})
		return
	}

	if err := target.Disconnect(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}

	sess, err := s.agent.NewSession(req.CWD, req.Model)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Apply thinking level from settings
	sm := config.GetSettingsManager()
	if level := sm.ThinkingLevel(); level != "" && level != "off" {
		_ = sess.SwitchThinking(level)
	}

	proxy := newSessionProxy(sess)

	s.mu.Lock()
	s.sessions[sess.ID()] = proxy
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, sess.Meta())
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not active"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}

// handleResumeSession reactivates a persisted session.
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session is not active"})
		return
	}
	proxy.session.Stop()
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session is not active"})
		return
	}
	messages := proxy.session.AllMessages()
	writeJSON(w, http.StatusOK, messages)
}

func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Already active?
	s.mu.RLock()
	if _, ok := s.sessions[id]; ok {
		s.mu.RUnlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is already active"})
		return
	}
	s.mu.RUnlock()

	sess, err := s.agent.ResumeSession(id)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	proxy := newSessionProxy(sess)
	s.mu.Lock()
	s.sessions[sess.ID()] = proxy
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, sess.Meta())
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Check in-memory first (active sessions)
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if ok {
		writeJSON(w, http.StatusOK, proxy.session.Meta())
		return
	}

	// Fallback: check store (persisted sessions from previous runs)
	sessions, err := s.agent.ListAllSessions()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, s := range sessions {
		if s.ID == id {
			writeJSON(w, http.StatusOK, s)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
}

type promptRequest struct {
	Text   string            `json:"text"`
	Images []types.ImageData `json:"images,omitempty"`
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	var req promptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Text == "" && len(req.Images) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text or images is required"})
		return
	}

	// Validate vision support if images are provided
	if len(req.Images) > 0 {
		meta := proxy.session.ModelMeta()
		if meta == nil || !meta.Vision {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "current model does not support images"})
			return
		}
	}

	ps := proxy.session.Prompt(context.Background(), req.Text, req.Images...)

	status := "started"
	if ps == types.PromptQueued {
		status = "queued"
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": status})
}

// handleEvents streams agent events as SSE.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan []byte, 1024)
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
}

func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session is not active"})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session is not active"})
		return
	}

	var req execCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}

	switch req.Command {
	case "rename":
		name, _ := req.Params["name"].(string)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "param 'name' is required"})
			return
		}
		if err := proxy.session.Rename(name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case "thinking":
		level, _ := req.Params["level"].(string)
		if level == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "param 'level' is required"})
			return
		}
		// Validate + persist FIRST. Only if the level is accepted do we apply it
		// to the live session, so an invalid value never mutates session state.
		if err := config.GetSettingsManager().SetThinkingLevel(level); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, config.ErrInvalidThinkingLevel) {
				status = http.StatusUnprocessableEntity
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		if err := proxy.session.SwitchThinking(level); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case "model":
		model, _ := req.Params["model"].(string)
		if model == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "param 'model' is required"})
			return
		}
		if err := proxy.session.SwitchModel(context.Background(), model); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// Persist to settings
		_ = config.GetSettingsManager().SetActiveModel(model)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case "compact":
		busy := proxy.session.IsBusy()
		go proxy.session.Compact(context.Background()) //nolint
		status := "started"
		if busy {
			status = "queued"
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": status})

	default:
		// Check if it's a skill command: skill:<name>
		if strings.HasPrefix(req.Command, "skill:") {
			skillName := strings.TrimPrefix(req.Command, "skill:")
			content, err := proxy.session.ReadSkill(skillName)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "skill not found: " + skillName})
				return
			}
			// Build prompt: skill content + optional user prompt
			prompt := content
			if userPrompt, _ := req.Params["prompt"].(string); userPrompt != "" {
				prompt += "\n\n---\n\n" + userPrompt
			}
			ps := proxy.session.Prompt(context.Background(), prompt)
			status := "started"
			if ps == types.PromptQueued {
				status = "queued"
			}
			writeJSON(w, http.StatusAccepted, map[string]string{"status": status})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown command: " + req.Command})
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
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
