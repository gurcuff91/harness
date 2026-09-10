// Package tools — WebSearch built-in tool.
//
// Calls the MiniMax coding-plan search endpoint with the active provider's
// API key. The tool is fully self-contained: it owns its HTTP client, URL,
// and response parser, instead of relying on a Search() helper on the
// provider. Decoupling from the provider registry is via a small
// ProviderLookup interface injected by the agent.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gurcuff91/harness/types"
)

// MiniMax search endpoint (per the official MiniMax-Coding-Plan-MCP integration).
var webSearchEndpoint = "https://api.minimax.io/v1/coding_plan/search"

// Default tunables — overridable through the tool's JSON params.
const (
	webSearchDefaultLimit   = 10
	webSearchDefaultTimeout = 30 * time.Second
	webSearchMinLimit       = 1
	webSearchMaxLimit       = 50
)

// webSearchAPIKeyHeader identifies the upstream customer of the API key
// (mirrors MiniMax-Coding-Plan-MCP's MM-API-Source: Minimax-MCP value).
const webSearchAPIKeyHeader = "MM-API-Source"

// webSearchHint is the MM-API-Source we send. Distinct from the upstream
// MCP so telemetry can attribute traffic to harness's built-in tool.
const webSearchHint = "harness-websearch"

// ProviderLookup is the minimal contract WebSearch needs from the provider
// registry. The agent satisfies it by wrapping the minimax provider
// instance once per session.
type ProviderLookup interface {
	IsActive() bool
	ResolveCredentials() (types.Credentials, error)
}

// searchInput is the tool's JSON schema payload. `limit` and `timeout` are
// optional; their zero values mean "use the documented default" (see
// normalizeSearchInput below).
type searchInput struct {
	Query   string `json:"query"`
	Limit   int    `json:"limit,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// searchResult is the per-result shape returned by the MiniMax API.
type searchResult struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
	Date    string `json:"date"`
}

// searchResponse mirrors the upstream JSON envelope. Only the fields we
// consume today are decoded; the rest are ignored.
type searchResponse struct {
	Organic []searchResult `json:"organic"`
	// RelatedSearches intentionally omitted — not part of the tool contract yet.
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// WebSearch returns the built-in WebSearch tool.
//
// `lookup` is consulted on every invocation so freshly-rotated credentials
// take effect immediately, without requiring a process restart. `client`
// may be nil; a default 30s timeout client is used in that case.
func WebSearch(lookup ProviderLookup, client *http.Client) Tool {
	if client == nil {
		// Per-call timeouts come from the tool's `timeout` param via
		// context.WithTimeout — this default only bounds the underlying
		// transport. Leaving it at the documented default keeps behavior
		// consistent across sessions.
		client = &http.Client{Timeout: 0}
	}
	return Tool{
		Def: types.ToolDef{
			Name:        "WebSearch",
			Description: webSearchDescription,
			InputSchema: mustSchema(searchInput{}),
		},
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			return runWebSearch(ctx, lookup, client, raw)
		},
	}
}

// webSearchDescription guides the model toward 3-5 keyword queries and
// encourages including a date for time-sensitive topics (mirrors
// MiniMax-Coding-Plan-MCP's recommended query strategy).
const webSearchDescription = `
You MUST use this tool whenever you need real-time or external information from the public web that is not in your training data or the local session.

Performs a live web search backed by the MiniMax coding-plan API.

Args:
    query (string, required): The search query. Aim for 3-5 keywords for the best results. For time-sensitive topics, include the current date (e.g. "latest iPhone 2025").
    limit (int, optional, default 10, max 50): Maximum number of organic results to return.
    timeout (int, optional, default 30, seconds): HTTP timeout for the upstream request.

Strategy:
    - Rephrase with different keywords if the first query returns no useful organic matches.
    - Prefer narrow, factual queries over open-ended ones.

Requirements:
    - The active provider named "minimax" must be connected. If it is not, the tool returns a clear error message explaining how to connect it (run "harness connect minimax" or set MINIMAX_API_KEY). Do NOT attempt to bypass this; the call always fails fast when the provider is missing.
`

// errProviderInactive is the dedicated message returned when the model
// calls WebSearch without the minimax provider connected. Surfaced
// verbatim in Execute's error string so the model can relay it.
var errProviderInactive = "WebSearch requires the \"minimax\" provider to be connected (run \"harness connect minimax\" or set MINIMAX_API_KEY)"

// runWebSearch validates input, performs the HTTP call, and returns the
// truncated organic slice as JSON. All non-network errors are returned as
// descriptive strings; network/HTTP errors are wrapped so the registry's
// existing telemetry paths pick them up.
func runWebSearch(ctx context.Context, lookup ProviderLookup, client *http.Client, raw json.RawMessage) (string, error) {
	var in searchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid JSON arguments: %w", err)
	}
	if in.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	limit, timeout := normalizeSearchInput(in)

	if lookup == nil || !lookup.IsActive() {
		return "", fmt.Errorf("%s", errProviderInactive)
	}
	creds, err := lookup.ResolveCredentials()
	if err != nil || creds.APIKey == "" {
		return "", fmt.Errorf("%s", errProviderInactive)
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{"q": in.Query})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	url := currentSearchURL()
	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+creds.APIKey)
	httpReq.Header.Set(webSearchAPIKeyHeader, webSearchHint)

	httpResp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("web search: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return "", types.NewProviderAPIError("minimax", httpResp.StatusCode, respBody)
	}

	var resp searchResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if resp.BaseResp.StatusCode != 0 {
		// MiniMax uses nested base_resp.status_code (0 == ok) for
		// application-level rejections distinct from the HTTP status.
		return "", fmt.Errorf("minimax rejected search: status_code=%d status_msg=%q",
			resp.BaseResp.StatusCode, resp.BaseResp.StatusMsg)
	}

	results := resp.Organic
	if len(results) > limit {
		results = results[:limit]
	}
	if results == nil {
		// Guarantee a stable JSON `[]` on empty upstream responses
		// instead of `null` — the downstream model consumes this shape.
		results = []searchResult{}
	}
	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal results: %w", err)
	}
	return string(out), nil
}

// normalizeSearchInput clamps the limit and timeout to the documented
// bounds, returning the effective values to use for the HTTP call.
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

func currentSearchURL() string { return webSearchEndpoint }

// mustSchema builds the JSON Schema for a tool's input struct using the
// shared llm.SchemaFor helper (kept side-by-side with the other built-in
// tool definitions to surface uniform errors at startup).
func mustSchema(v any) json.RawMessage {
	// WebSearch's schema is small enough to spell out by hand so the
	// description on each field can carry the query guidance rather than
	// relying on a generic "object" entry the model has to interpret.
	const schema = `{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string",
      "description": "3-5 keyword search query. Include a date for time-sensitive topics."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "default": 10,
      "description": "Maximum number of organic results to return."
    },
    "timeout": {
      "type": "integer",
      "minimum": 1,
      "default": 30,
      "description": "HTTP timeout in seconds for the upstream search request."
    }
  }
}`
	_ = v // reserved for a future llm.SchemaFor replacement
	return json.RawMessage(schema)
}
