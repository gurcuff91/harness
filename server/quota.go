package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleProviderQuota serves GET /api/providers/{name}/quota[?refresh=true]:
// the provider's subscription usage in one format (types.Quota), cached. 404
// unknown provider or one without quota, 409 not connected, 502 the provider
// could not be read and there is no earlier reading.
func (s *Server) handleProviderQuota(w http.ResponseWriter, r *http.Request) {
	refresh, _ := strconv.ParseBool(r.URL.Query().Get("refresh"))
	q, retry, err := s.admin.Providers.Quota(r.Context(), chi.URLParam(r, "name"), refresh)
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((retry+time.Second-1)/time.Second)))
	}
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}
