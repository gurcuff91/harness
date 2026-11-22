package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gurcuff91/harness/internal/providers"
)

// forgetQuota drops a provider's cached reading (on connect/disconnect).
func forgetQuota(name string) { providers.ForgetQuota(name) }

// handleProviderQuota serves GET /api/providers/{name}/quota[?refresh=true]:
// the provider's subscription usage in one format (types.Quota), cached for
// quotaTTL. 404 unknown provider or one without quota, 409 not connected, 502
// the provider could not be read and there is no earlier reading.
func (s *Server) handleProviderQuota(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p := providers.Find(name)
	if p == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q not found", name), nil)
		return
	}
	if _, ok := p.(providers.QuotaReporter); !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q does not report a quota", name), nil)
		return
	}
	refresh, _ := strconv.ParseBool(r.URL.Query().Get("refresh"))
	q, retry, err := providers.CachedQuota(r.Context(), name, refresh)
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((retry+time.Second-1)/time.Second)))
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, q)
	case errors.Is(err, providers.ErrNoUpstream):
		writeError(w, http.StatusConflict, fmt.Sprintf("provider %q is not connected (or has no valid credentials): connect it first", name), nil)
	case errors.Is(err, providers.ErrNoQuota):
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q has no quota for this account", name), nil)
	default:
		writeError(w, http.StatusBadGateway, err.Error(), nil)
	}
}
