package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

// Defaults — overridable through the tool's JSON params. Defaults follow
// the lighter end of each backend's documented range (MiniMax accepts up
// to ~20 organic entries in practice; Ollama caps at 10), so a single
// default serves both without surprises.
const (
	webSearchDefaultTimeout = 30 * time.Second
	webSearchDefaultLimit   = 5
	webSearchMinLimit       = 1
	webSearchMaxLimit       = 10
)

// snippetMaxChars caps the Ollama content field — its API can return
// full-page excerpts (~10k chars). MiniMax snippets are short by design
// and pass through untouched.
const snippetMaxChars = 500

// minimaxAPIKeyHeader/minimaxHint identify the upstream customer of the
// API key (mirrors MiniMax-Coding-Plan-MCP's MM-API-Source header),
// distinct from that MCP so traffic can be attributed to harness's own
// built-in tool.
const (
	minimaxAPIKeyHeader = "MM-API-Source"
	minimaxHint         = "harness-websearch"
)

// searchInput is the JSON input schema for the WebSearch tool.
type searchInput struct {
	Query   string `json:"query" validate:"required"`
	Limit   int    `json:"limit,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// searchResult is the per-result normalized shape, identical regardless of
// which backend answered.
type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchBackend is one ready-to-use HTTP backend for the search dispatcher.
// Name is for internal dispatch/diagnostics ONLY — the tool never embeds
// it in the user-facing error string or output (best-effort, no
// infrastructure leaks to the model or to the TUI).
type SearchBackend struct {
	Name string
	URL  string
	Key  string
}

// ProviderLookup is the minimal contract the WebSearch tool needs from the
// agent. The agent passes a list of currently-active backends; the tool
// dispatches against them in order without ever importing the providers
// package directly (keeps the backend/frontend separation described in
// AGENTS.md intact: agent/tools must never depend on internal/providers).
type ProviderLookup interface {
	ActiveSearchBackends() []SearchBackend
}

// WebSearch returns the WebSearch tool. lookup is consulted at each call
// for active backends (so a freshly-connected provider starts being used
// immediately). client may be nil; per-backend timeouts come from the
// tool's `timeout` param and bound the context, not the shared client.
func WebSearch(lookup ProviderLookup, client *http.Client) Tool {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return Tool{
		Def: types.ToolDef{
			Name:        "WebSearch",
			Description: "Search the public web for up-to-date information that is not in your training data or the local session. Use this whenever you need current facts, news, prices, or anything else that may have changed since your training cutoff.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "3-5 keyword search query. Include a date for time-sensitive topics (e.g. \"latest iPhone 2025\")."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 10, "description": "Maximum number of results to return (default: 5)."},
					"timeout": {"type": "integer", "minimum": 1, "description": "Per-backend HTTP timeout in seconds (default: 30)."}
				},
				"required": ["query"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args searchInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if err := requireFields(&args); err != nil {
				return err.Error(), err
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("query is required")
			}
			return runWebSearch(ctx, lookup, client, args)
		},
	}
}

// runWebSearch is the dispatcher: it fans out to each active backend in
// order, stopping at the first one that returns non-empty results.
func runWebSearch(ctx context.Context, lookup ProviderLookup, httpClient *http.Client, args searchInput) (string, error) {
	limit, timeout := normalizeSearchInput(args)

	var backends []SearchBackend
	if lookup != nil {
		backends = lookup.ActiveSearchBackends()
	}
	if len(backends) == 0 {
		return "", fmt.Errorf("WebSearch needs at least one provider connected: `minimax` or `ollama-cloud`")
	}

	// Per-backend dispatch loop. Each backend gets its own context.WithTimeout
	// so a stuck backend can't consume the whole user budget. Failures fall
	// through to the next backend unless they produced a non-empty result.
	var reasons []string
	for _, b := range backends {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		results, reason, err := runOneBackend(reqCtx, b, httpClient, args.Query, limit)
		cancel()

		if err == nil && len(results) > 0 {
			return renderResults(results)
		}
		// Two distinct outcomes recorded separately:
		//   - backend returned a structured, empty result (success-with-zero-
		//     data): no error, no reason — continue to give the next backend
		//     a chance.
		//   - backend errored (err != nil): record the categorized reason for
		//     the aggregated final error in case no backend produces data.
		if err != nil {
			reasons = append(reasons, reason)
		}
	}

	// No backend produced results. Distinguish "all backends returned
	// cleanly but empty" (legitimate no-result answer: `[]`) from "at least
	// one backend failed" (error to surface to the user).
	if len(reasons) == 0 {
		return "[]", nil
	}
	deduped := dedupeReasons(reasons)
	return "", fmt.Errorf("web search failed: no results from any backend\n\n- %s", strings.Join(deduped, "\n- "))
}

// runOneBackend fires a single backend, normalizes the response, and
// returns either the slice of normalized results or a short, infra-leak-
// free failure category plus the underlying error.
func runOneBackend(ctx context.Context, b SearchBackend, httpClient *http.Client, query string, limit int) ([]searchResult, string, error) {
	body, err := buildBackendRequest(b, query)
	if err != nil {
		return nil, "request build failed", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "request build failed", err
	}
	if b.Key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+b.Key)
	}
	addBackendHeaders(httpReq, b)

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, classifyTransportError(err), err
	}
	defer httpResp.Body.Close()
	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("provider rejected the request (status %d)", httpResp.StatusCode),
			types.NewProviderAPIError(b.Name, httpResp.StatusCode, respBody)
	}

	results, err := parseBackendResponse(b, respBody, limit)
	if err != nil {
		// base_resp.status_code != 0 lands here (MiniMax only — Ollama has
		// no equivalent layer). Not a real HTTP failure, but the backend
		// said "no" — same coarse category as a 4xx.
		return nil, "provider rejected the request (status 0)", err
	}
	return results, "", nil
}

// ── Per-backend request/response translation ──────────────────────────────
//
// MiniMax:  POST https://api.minimax.io/v1/coding_plan/search
//           body {"q": "<query>"}, header MM-API-Source: harness-websearch
//           response {organic:[{title,link,snippet,date}], base_resp:{status_code,status_msg}}
// Ollama:   POST https://ollama.com/api/web_search
//           body {"query": "<query>", "max_results": N}
//           response {results:[{title,url,content}]}
//
// Both are normalized to searchResult{title, url, snippet} — MiniMax's
// "link" becomes "url" and its "date" is dropped; Ollama's "content"
// becomes "snippet", truncated to snippetMaxChars.

func buildBackendRequest(b SearchBackend, query string) ([]byte, error) {
	switch b.Name {
	case "ollama-cloud":
		return json.Marshal(map[string]any{
			"query":       query,
			"max_results": webSearchMaxLimit,
		})
	case "minimax":
		return json.Marshal(map[string]string{"q": query})
	default:
		return nil, fmt.Errorf("unsupported backend: %s", b.Name)
	}
}

func addBackendHeaders(req *http.Request, b SearchBackend) {
	req.Header.Set("Content-Type", "application/json")
	if b.Name == "minimax" {
		req.Header.Set(minimaxAPIKeyHeader, minimaxHint)
	}
}

func parseBackendResponse(b SearchBackend, body []byte, limit int) ([]searchResult, error) {
	switch b.Name {
	case "ollama-cloud":
		return parseOllamaResponse(body, limit)
	case "minimax":
		return parseMinimaxResponse(body, limit)
	default:
		return nil, fmt.Errorf("unsupported backend: %s", b.Name)
	}
}

type minimaxResponse struct {
	Organic  []minimaxItem `json:"organic"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

type minimaxItem struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
	// Date is parsed but intentionally discarded — the normalized output
	// shape doesn't carry a date field (Ollama has no equivalent).
	Date string `json:"date,omitempty"`
}

func parseMinimaxResponse(body []byte, limit int) ([]searchResult, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var resp minimaxResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.BaseResp.StatusCode != 0 {
		return nil, fmt.Errorf("provider rejected the request (status %d): %s",
			resp.BaseResp.StatusCode, strings.TrimSpace(resp.BaseResp.StatusMsg))
	}
	out := make([]searchResult, 0, len(resp.Organic))
	for _, item := range resp.Organic {
		out = append(out, searchResult{Title: item.Title, URL: item.Link, Snippet: item.Snippet})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type ollamaResponse struct {
	Results []ollamaItem `json:"results"`
}

type ollamaItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func parseOllamaResponse(body []byte, limit int) ([]searchResult, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var resp ollamaResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	out := make([]searchResult, 0, len(resp.Results))
	for _, item := range resp.Results {
		out = append(out, searchResult{Title: item.Title, URL: item.URL, Snippet: truncateSnippet(item.Content)})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// truncateSnippet caps a long snippet at snippetMaxChars, suffixing "…".
// A short summary is plenty for citation; full content is one Fetch call away.
func truncateSnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= snippetMaxChars {
		return s
	}
	return s[:snippetMaxChars] + "…"
}

// ── Error classification & shared helpers ──────────────────────────────────

// classifyTransportError translates low-level errors into short, infra-
// leak-free categories used in the aggregated user-facing error.
func classifyTransportError(err error) string {
	if err == nil {
		return ""
	}
	if cat := ctxErrCategory(err); cat != "" {
		return cat
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "no such host"):
		return "network unreachable"
	default:
		return "transport error"
	}
}

// ctxErrCategory maps a context/timeout error to a short category. Uses
// errors.Is/As for canonical recognition (context.DeadlineExceeded,
// context.Canceled, any net.Error with Timeout()==true), falling back to
// string-matching for errors the stdlib doesn't wrap cleanly.
func ctxErrCategory(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "request cancelled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "request timed out"
	}
	msg := err.Error()
	if strings.Contains(msg, "context deadline exceeded") {
		return "request timed out"
	}
	if strings.Contains(msg, "context canceled") {
		return "request cancelled"
	}
	return ""
}

// dedupeReasons collapses repeated reason strings, preserving first-seen
// order. Keeps the aggregated error readable when two backends fail the
// same way (e.g. both time out).
func dedupeReasons(reasons []string) []string {
	seen := make(map[string]struct{}, len(reasons))
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// normalizeSearchInput clamps limit and timeout to the documented bounds.
func normalizeSearchInput(in searchInput) (limit int, timeout time.Duration) {
	limit = in.Limit
	switch {
	case limit <= 0:
		limit = webSearchDefaultLimit
	case limit > webSearchMaxLimit:
		limit = webSearchMaxLimit
	}
	if limit < webSearchMinLimit {
		limit = webSearchMinLimit
	}

	t := time.Duration(in.Timeout) * time.Second
	if t <= 0 {
		t = webSearchDefaultTimeout
	}
	return limit, t
}

// renderResults guarantees a JSON `[]` (never `null`) for empty slices —
// the model consumes the shape uniformly across both backends.
func renderResults(results []searchResult) (string, error) {
	if results == nil {
		results = []searchResult{}
	}
	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal results: %w", err)
	}
	return string(out), nil
}
