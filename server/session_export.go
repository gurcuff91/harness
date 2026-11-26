package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// DefaultImportMaxBodyBytes caps the body of POST /api/sessions/import
// (ServerOptions.ImportMaxBodyBytes overrides it).
const DefaultImportMaxBodyBytes = 1 << 30

// handleExportSession serves GET /api/sessions/{id}/export: the session as a
// JSONL export (a header, then every message), streamed — open or closed.
func (s *Server) handleExportSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	meta, err := s.exportMeta(id)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+admin.ExportFileName(meta)+`"`)
	w.WriteHeader(http.StatusOK)
	if _, err := s.admin.Sessions.ExportAs(meta, w); err != nil {
		// The status is already sent: a broken stream is all the client sees.
		s.logger.Error("server", "export", "session", id, "error", err.Error())
	}
}

// exportMeta is the meta an export carries: the LIVE one (current model,
// thinking, stats) for a session open here, else the stored one
// (admin.ErrNotFound when there is none).
func (s *Server) exportMeta(id string) (store.SessionMeta, error) {
	if p, ok := s.liveProxy(id); ok {
		return p.session.Meta(), nil
	}
	return s.admin.Sessions.Get(id)
}

// handleImportSession serves POST /api/sessions/import[?cwd=]: the body is a
// session export (JSONL); it is opened as a NEW session (201, like a fork).
func (s *Server) handleImportSession(w http.ResponseWriter, r *http.Request) {
	limit := s.importMaxBody
	if limit <= 0 {
		limit = DefaultImportMaxBodyBytes
	}
	var opts []agent.SessionOption
	for _, raw := range r.URL.Query()["channel"] {
		typ, ref, _ := strings.Cut(raw, ":")
		opts = append(opts, agent.SessionWithChannels(types.InputChannel{Type: typ, Ref: ref}))
	}
	sess, err := s.agent.ImportSession(http.MaxBytesReader(w, r.Body, limit), r.URL.Query().Get("cwd"), opts...)
	if err != nil {
		s.writeImportErr(w, err, limit)
		return
	}
	s.mu.Lock()
	s.sessions[sess.ID()] = newSessionProxy(sess, s.logger)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, liveDetail(sess))
}

func (s *Server) writeImportErr(w http.ResponseWriter, err error, limit int64) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("session export exceeds the %d byte import limit", limit), nil)
	case errors.Is(err, admin.ErrInvalidExport):
		writeErr(w, http.StatusBadRequest, err)
	case channelErrStatus(err, 0) != 0:
		writeErr(w, channelErrStatus(err, 0), err)
	case errors.Is(err, agent.ErrNoModelAvailable):
		writeErr(w, http.StatusConflict, err)
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}

// exportsDir is where the export command writes (a variable for tests).
var exportsDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".harness", "exports"), nil
}

// commandExport runs the session command "export": the server writes the
// session to ~/.harness/exports/<file> (dir 0700, file 0600, through a temp
// file renamed at the end) and answers the path and count. It takes no path:
// nobody can make the server write anywhere else.
func (s *Server) commandExport(sess *agent.Session) (string, error) {
	dir, err := exportsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".export-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	n, err := s.admin.Sessions.ExportAs(sess.Meta(), tmp)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, admin.ExportFileName(sess.Meta()))
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s (%d messages)", path, n), nil
}

// commandImport runs the session command "import" {path}: the server reads that
// export (refused before anything is created unless it is one) and opens it as
// a NEW session in the calling session's project; the message is its id.
func (s *Server) commandImport(sess *agent.Session, path string) (string, error) {
	if !strings.HasSuffix(path, ".export.jsonl") {
		return "", fmt.Errorf("%w: %q is not a .export.jsonl file", admin.ErrInvalidExport, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", admin.ErrInvalidExport, err)
	}
	defer f.Close()
	imp, err := s.agent.ImportSession(f, sess.CWD())
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[imp.ID()] = newSessionProxy(imp, s.logger)
	s.mu.Unlock()
	return imp.ID(), nil
}
