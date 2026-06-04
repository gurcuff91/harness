package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gurcuff91/harness/agent"
)

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
	r.Post("/api/sessions", s.handleCreateSession)
	r.Get("/api/sessions/{id}", s.handleGetSession)
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

type createSessionResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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

	writeJSON(w, http.StatusCreated, createSessionResponse{
		ID:   sess.ID(),
		Name: sess.Name(),
	})
}

type sessionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	proxy, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	writeJSON(w, http.StatusOK, sessionInfo{
		ID:   id,
		Name: proxy.session.Name(),
	})
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

	proxy.session.Prompt(context.Background(), req.Text)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
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
