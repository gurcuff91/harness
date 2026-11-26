package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// adminKindStatus is the HTTP status of each admin error kind (admin.Kind: the
// taxonomy lives there, once). A kind absent here — the provider lookups and
// name conflicts, invalid exports — keeps the generic 500 it always had where
// a handler does not map it itself.
var adminKindStatus = map[string]int{
	"not_found":              http.StatusNotFound,
	"unprocessable":          http.StatusUnprocessableEntity,
	"invalid":                http.StatusBadRequest,
	"session_active":         http.StatusConflict,
	"conflict":               http.StatusConflict,
	"not_connected":          http.StatusConflict,
	"provider_not_removable": http.StatusConflict,
	"upstream":               http.StatusBadGateway,
	"not_configured":         http.StatusServiceUnavailable,
}

// adminStatus maps an admin error to its HTTP status through admin.Kind (so
// "unprocessable" wins over "invalid" for the errors that carry both).
func adminStatus(err error) int {
	if st, ok := adminKindStatus[admin.Kind(err)]; ok {
		return st
	}
	return http.StatusInternalServerError
}

// writeAdminErr answers err with the status adminStatus gives it.
func writeAdminErr(w http.ResponseWriter, err error) { writeErr(w, adminStatus(err), err) }

// ── Settings ─────────────────────────────────────────────────────────────

// SettingsDTO is the API representation of harness settings (the admin type:
// its JSON tags are the single source of truth for the contract).
type SettingsDTO = admin.SettingsValues

// handleSettings returns current settings with defaults.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.admin.Settings.Get())
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
	if body.ActiveModel != nil {
		if err := s.admin.Settings.SetActiveModel(*body.ActiveModel); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if body.ThinkingLevel != nil {
		if err := s.admin.Settings.SetThinkingLevel(*body.ThinkingLevel); err != nil {
			writeAdminErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.admin.Settings.Get())
}

// ── MCP servers (settings collection) ───────────────────────────────────

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.admin.MCP.List())
}

func (s *Server) handlePutMCPServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var srv types.MCPServer
	if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	if err := s.admin.MCP.Put(name, srv); err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

func (s *Server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if err := s.admin.MCP.Delete(chi.URLParam(r, "name")); err != nil {
		writeAdminErr(w, err)
		return
	}
	writeStatus(w, http.StatusOK, "deleted", "")
}

// ── Custom providers ─────────────────────────────────────────────────────

func (s *Server) handleListCustomProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.admin.Providers.ListCustom())
}

func (s *Server) handlePutCustomProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var p types.CustomProvider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	if err := s.admin.Providers.PutCustom(name, p); err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteCustomProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.admin.Providers.DeleteCustom(chi.URLParam(r, "name")); err != nil {
		writeAdminErr(w, err)
		return
	}
	writeStatus(w, http.StatusOK, "deleted", "")
}

// handleReloadCustomProviders applies settings.json's "provider" collection
// live. 200 {"status":{"code":"reloaded"}} — with a message listing any entries
// skipped because a programmatic provider already uses their name (code
// "reloaded_with_conflicts").
func (s *Server) handleReloadCustomProviders(w http.ResponseWriter, r *http.Request) {
	if err := s.admin.Providers.ReloadCustom(); err != nil {
		if errors.Is(err, admin.ErrProviderNameConflict) {
			writeStatus(w, http.StatusOK, "reloaded_with_conflicts", err.Error())
			return
		}
		writeAdminErr(w, err)
		return
	}
	writeStatus(w, http.StatusOK, "reloaded", "")
}

// ── Schedules ────────────────────────────────────────────────────────────

// handleListSchedules serves the read-only list of cron-scheduled prompts, in
// the same shape as the ScheduleList tool (slug, cron, prompt, runs, last_run).
// Optional ?owner=<session_id> filters to the schedules that fire into it.
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := s.admin.Schedules.List(r.URL.Query().Get("owner"))
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	type entry struct {
		Slug    string `json:"slug"`
		Cron    string `json:"cron"`
		Prompt  string `json:"prompt"`
		Runs    int    `json:"runs"`
		LastRun int64  `json:"last_run,omitempty"`
	}
	out := make([]entry, 0, len(list))
	for _, sc := range list {
		out = append(out, entry{sc.Slug, sc.Cron, sc.Prompt, sc.Runs, sc.LastRun})
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Memories ─────────────────────────────────────────────────────────────

// handleListMemories lists/searches memories. All params are optional:
//
//	cwd, query, include_content (default true), skip (default 0), limit (default 10)
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := s.admin.Memory.Search(q.Get("cwd"), q.Get("query"), q.Get("include_content") != "false",
		atoiDefault(q.Get("skip"), 0), atoiDefault(q.Get("limit"), 10))
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// memoryTarget resolves the {slug} and ?cwd= of a single-memory request. The
// slug is URL-unescaped here (chi matches on the RAW path, so "a/b" arrives as
// "a%2Fb"). ok=false means a response was already written.
func (s *Server) memoryTarget(w http.ResponseWriter, r *http.Request) (cwd, slug string, ok bool) {
	if !s.admin.Memory.Configured() {
		writeError(w, http.StatusServiceUnavailable, "memory is not enabled on this agent", nil)
		return "", "", false
	}
	slug, err := url.PathUnescape(chi.URLParam(r, "slug"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug: "+err.Error(), nil)
		return "", "", false
	}
	return r.URL.Query().Get("cwd"), slug, true
}

func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	cwd, slug, ok := s.memoryTarget(w, r)
	if !ok {
		return
	}
	m, err := s.admin.Memory.Get(cwd, slug)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handlePutMemory creates or replaces one memory (an upsert by cwd+slug):
// 201 when created, 200 when updated; the body is the stored memory.
func (s *Server) handlePutMemory(w http.ResponseWriter, r *http.Request) {
	cwd, slug, ok := s.memoryTarget(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	m, created, err := s.admin.Memory.Put(cwd, slug, body.Content)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, m)
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	cwd, slug, ok := s.memoryTarget(w, r)
	if !ok {
		return
	}
	if err := s.admin.Memory.Delete(cwd, slug); err != nil {
		writeAdminErr(w, err)
		return
	}
	writeStatus(w, http.StatusOK, "deleted", "")
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

// ── Providers, models, quota ─────────────────────────────────────────────

// providerInfo is the API representation of a provider: admin's, plus where
// its transparent proxy lives on this server.
type providerInfo struct {
	admin.ProviderInfo
	// ProxyPath is the provider's transparent-proxy root on this server
	// (/api/providers/{name}/proxy), to be appended to the server's base URL.
	ProxyPath string `json:"proxy_path"`
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	infos, err := s.admin.Providers.List(r.Context())
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	list := make([]providerInfo, 0, len(infos))
	for _, in := range infos {
		list = append(list, providerInfo{ProviderInfo: in, ProxyPath: "/api/providers/" + url.PathEscape(in.Name) + "/proxy"})
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleConnectProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	// An unknown provider is a 404 before the body is even read.
	if _, err := s.admin.Providers.CredentialType(name); err != nil {
		writeAdminErr(w, err)
		return
	}
	var req admin.ConnectCredentials
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error(), nil)
		return
	}
	n, err := s.admin.Providers.Connect(r.Context(), name, req)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeStatus(w, http.StatusOK, "connected", strconv.Itoa(n)+" models")
}

func (s *Server) handleDisconnectProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.admin.Providers.Disconnect(chi.URLParam(r, "name")); err != nil {
		writeAdminErr(w, err)
		return
	}
	writeStatus(w, http.StatusOK, "disconnected", "")
}

// handleModels handles GET /api/models. ?provider=<name> restricts the listing
// to one provider — and, unlike the unfiltered call (where a failing provider
// just contributes nothing), says what is wrong with it: 404 unknown, 409 not
// connected, 502 its model fetch failed.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("provider")
	if name == "" && r.URL.Query().Has("provider") {
		writeError(w, http.StatusBadRequest, "provider must not be empty", nil)
		return
	}
	list, err := s.admin.Providers.Models(r.Context(), name)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleModel handles GET /api/models/{provider}/{id}: one model's listing. The
// id is the provider-local one and may itself contain slashes or colons
// ("org/model", "gemma3:4b"), hence the wildcard.
func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "provider")
	id, err := url.PathUnescape(chi.URLParam(r, "*"))
	if err != nil || id == "" {
		writeError(w, http.StatusBadRequest, "invalid model id", nil)
		return
	}
	m, err := s.admin.Providers.Model(r.Context(), name, id)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// ── Stored sessions ──────────────────────────────────────────────────────

// handleListSessions returns all sessions, optionally filtered by ?cwd=
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.admin.Sessions.List(r.URL.Query().Get("cwd"))
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

// handleDeleteSession deletes a session permanently. A session open here is
// closed first (admin refuses to delete an open one); an unknown id is not an
// error (204).
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	s.mu.Lock()
	proxy, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
		proxy.close()
	}
	s.mu.Unlock()

	if err := s.admin.Sessions.Delete(id); err != nil && !errors.Is(err, admin.ErrNotFound) {
		writeAdminErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// liveProxy returns the session's proxy when it is open on this server.
func (s *Server) liveProxy(id string) (*SessionProxy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.sessions[id]
	return p, ok
}

// sessionMessages reads a window of a session's log: the live session's when it
// is open here, else the stored one.
func (s *Server) sessionMessages(id string, win store.MessageWindow) ([]types.Message, int, int, error) {
	if p, ok := s.liveProxy(id); ok {
		return p.session.MessagesWindow(win)
	}
	return s.admin.Sessions.Messages(id, win)
}

// handleGetMessages returns a session's message history — all of it by
// default, or a suffix of it:
//
//	?from=<n>         start at absolute index n (n >= total → empty array)
//	?compactions=<k>  start just after the k-th-to-last compaction checkpoint
//
// from and compactions are mutually exclusive (400 if both are present). The
// body is always a plain JSON array; X-Messages-From carries the absolute index
// of its first element and X-Messages-Total the length of the full log.
func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	q := r.URL.Query()
	win, errMsg := parseMessageWindow(q.Get("from"), q.Has("from"), q.Get("compactions"), q.Has("compactions"))
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg, nil)
		return
	}
	messages, from, total, err := s.sessionMessages(id, win)
	if err != nil {
		if errors.Is(err, admin.ErrNotFound) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "session not found", nil)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	if messages == nil {
		messages = []types.Message{}
	}
	w.Header().Set("X-Messages-From", strconv.Itoa(from))
	w.Header().Set("X-Messages-Total", strconv.Itoa(total))
	writeJSON(w, http.StatusOK, messages)
}

// sessionEpoch identifies one "life" of a session's message log for caching:
// its ResetCount (each /reset empties the log and reuses indices).
func (s *Server) sessionEpoch(id string) string {
	if p, ok := s.liveProxy(id); ok {
		return "r" + strconv.Itoa(p.session.Meta().ResetCount)
	}
	return "r" + strconv.Itoa(s.admin.Sessions.ResetCount(id))
}
