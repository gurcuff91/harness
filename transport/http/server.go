package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/config"
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
	r.Get("/api/providers", s.handleProviders)
	r.Get("/api/models", s.handleModels)
	r.Get("/api/sessions", s.handleListSessions)
	r.Post("/api/sessions", s.handleCreateSession)
	r.Get("/api/sessions/{id}", s.handleGetSession)
	r.Delete("/api/sessions/{id}", s.handleDeleteSession)
	r.Post("/api/sessions/{id}/close", s.handleCloseSession)
	r.Post("/api/sessions/{id}/prompt", s.handlePrompt)
	r.Get("/api/sessions/{id}/events", s.handleEvents)

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

// handleSettings returns current settings with defaults.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	sm := config.GetSettingsManager()
	model := sm.ActiveModel()
	thinking := sm.ThinkingLevel()
	if thinking == "" {
		thinking = "off"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"active_model":  model,
		"thinking_level": thinking,
	})
}

// providerInfo is the API representation of a provider.
type providerInfo struct {
	Name         string `json:"name"`
	Active       bool   `json:"active"`
	Activation   string `json:"activation"`
	IsSubscription bool `json:"is_subscription"`
	ModelCount   int    `json:"model_count"`
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
			Active:         p.IsActive(),
			Activation:     act,
			IsSubscription: p.CredentialType() == types.CredTypeOAuth,
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

type sessionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Check in-memory first (active sessions)
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if ok {
		writeJSON(w, http.StatusOK, sessionInfo{
			ID:   id,
			Name: proxy.session.Name(),
		})
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
	Text string `json:"text"`
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
	if req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}

	busy := proxy.session.IsBusy()
	proxy.session.Prompt(context.Background(), req.Text)

	status := "started"
	if busy {
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

	ch := make(chan []byte, 64)
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
