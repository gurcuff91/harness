package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gurcuff91/harness/internal/oauthflow"
)

// oauthRequest is the single body shape POST /api/oauth/{provider} accepts
// for BOTH phases. An absent/empty ExchangeCode means "Start"; a non-empty
// one means "Exchange". This mirrors oauthflow.OauthFlow's two-phase split
// (Start/Exchange) behind one REST endpoint and one HTTP verb, per the
// design doc (docs/plans/2026-09-11-oauth-api-endpoint-design.md).
type oauthRequest struct {
	ExchangeCode string `json:"exchange_code,omitempty"`
	VerifierCode string `json:"verifier_code,omitempty"`
}

// handleOAuth drives a provider's native OAuth PKCE flow statelessly across
// two separate HTTP requests:
//
//   - Phase 1 (Start): body has no exchange_code. Builds a FRESH
//     oauthflow.For(provider) flow, calls Start(), and returns
//     {"auth_url", "verifier_code"}. The server does not open a browser —
//     that is the caller's job (CLI/TUI, via internal/browseropen).
//   - Phase 2 (Exchange): body has exchange_code + verifier_code. Builds
//     another fresh flow instance and calls Exchange(exchange_code,
//     verifier_code), returning the raw credentials. The caller is
//     responsible for a SEPARATE call to POST /api/providers/{name}/connect
//     to persist them — this endpoint never calls Connect itself.
//
// The handler holds no OAuth state between requests: each call constructs
// its own oauthflow.OauthFlow instance. The one exception is codex-oauth's
// local callback listener (localhost:1455), which is entirely internal to
// that flow's own Start() — see internal/oauthflow/codex.go.
func (s *Server) handleOAuth(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")

	flow, err := oauthflow.For(provider)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), nil)
		return
	}

	var req oauthRequest
	// An empty body is valid for Start — Decode's EOF error is ignored, not
	// surfaced, so "no body at all" and "{}" behave identically.
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.ExchangeCode == "" {
		authURL, verifierCode, err := flow.Start()
		if err != nil {
			// Covers codex-oauth's port-1455-busy case (a login already in
			// progress) — 409 Conflict, not 500: it's a legitimate,
			// expected contention state, not a server malfunction.
			writeError(w, http.StatusConflict, err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"auth_url":      authURL,
			"verifier_code": verifierCode,
		})
		return
	}

	if req.VerifierCode == "" {
		writeError(w, http.StatusBadRequest, "verifier_code is required for exchange", nil)
		return
	}

	creds, err := flow.Exchange(req.ExchangeCode, req.VerifierCode)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, creds)
}
