package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/oauthflow"
	llm "github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// ── Constants ────────────────────────────────────────────────────────────

// The ChatGPT Codex backend — the only endpoint the OAuth (ChatGPT-plan)
// token works against (api.openai.com rejects it; the standard openai
// provider covers that route). The Responses dialect, NOT Chat Completions.
const (
	codexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	codexModelsURL    = "https://chatgpt.com/backend-api/codex/models"
	codexTokenURL     = "https://auth.openai.com/oauth/token"
	codexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann" // Codex CLI's public PKCE client

	// codexDefaultExpiresIn mirrors oauthflow's — used when the token
	// endpoint omits expires_in (~1h in practice).
	codexDefaultExpiresIn = 3600

	// codexOriginator is the FIRST-PARTY originator the backend allowlists
	// (codex_cli_rs / codex_vscode / codex_sdk_ts / "Codex " prefix). An
	// unregistered originator gets 403 on every request — see pi issue #1828.
	// Symmetric with claude-oauth's "fingimos ser Claude Code": harness
	// masquerades as the Codex CLI, and the shared public PKCE client id
	// above is exactly what the CLI itself uses (public by OAuth design —
	// security rests on PKCE, not on the id being secret).
	codexOriginator = "codex_cli_rs"
)

// codexVersion feeds both the User-Agent and /models' client_version query
// param — the same server-side per-model minimal_client_version gating that
// bit harness on the Claude side (claude_code_version_too_old, fixed in
// v0.76.63 via CLAUDE_CLI_VERSION). Hardcode + env override, same pattern.
var codexVersion = envOrDefault("CODEX_CLI_VERSION", "0.144.0")

// codexRefreshClient: bounded timeout, for the same reason claude-oauth's
// refresh client is bounded — the refresh runs while holding the cross-
// process credentials lock, and an unbounded hang there would let the
// stale-lock reclaim steal the lock from this still-alive process.
var codexRefreshClient = &http.Client{Timeout: 30 * time.Second}

// ── CodexOAuth ───────────────────────────────────────────────────────────

// CodexOAuth implements Provider using the Codex CLI's ChatGPT subscription.
// Makes requests indistinguishable from codex_cli_rs — uses the ChatGPT
// plan's included usage instead of paying API rates.
type CodexOAuth struct {
	client  *http.Client
	tokens  *codexTokenManager
	session string // stable session ID per harness instance — also the prompt-cache steering hint
	cache   map[string]types.ModelMeta
	mu      sync.RWMutex
}

func NewCodexOAuth() (*CodexOAuth, error) {
	c := &CodexOAuth{
		client:  &http.Client{Timeout: 5 * time.Minute},
		tokens:  newCodexTokenManager(),
		session: uuid.New().String(),
		cache:   make(map[string]types.ModelMeta),
	}
	return c, nil
}

func (c *CodexOAuth) Name() string                         { return "codex-oauth" }
func (c *CodexOAuth) DisplayName() string                  { return "Codex OAuth" }
func (c *CodexOAuth) Description() string                  { return describeState(c) }
func (c *CodexOAuth) CredentialType() types.CredentialType { return types.CredTypeOAuth }

func (c *CodexOAuth) ActivationSource() ActivationSource {
	if _, err := c.ResolveCredentials(); err == nil {
		return ActivationCredentials
	}
	return ActivationNone
}
func (c *CodexOAuth) IsActive() bool {
	_, err := c.ResolveCredentials()
	return err == nil
}

// ── Credential management ────────────────────────────────────────────────

// ResolveCredentials reads from the credential chain AND validates the token.
// Chain: cache → credentials.json. Refreshes expired tokens automatically.
func (c *CodexOAuth) ResolveCredentials() (types.Credentials, error) {
	if err := c.loadCredentialsFromSources(); err != nil {
		return types.Credentials{}, err
	}
	tok, err := c.tokens.getValidToken()
	if err != nil {
		return types.Credentials{}, fmt.Errorf("codex-oauth credentials invalid or expired: %w", err)
	}
	creds := *c.tokens.creds
	creds.AccessToken = tok
	return creds, nil
}

func (c *CodexOAuth) loadCredentialsFromSources() error {
	c.tokens.mu.Lock()
	validating := c.tokens.validating
	c.tokens.mu.Unlock()

	if !validating {
		if cred, ok := config.GetCredentialsManager().Credential("codex-oauth"); ok && cred.AccessToken != "" {
			creds := codexCredFromDisk(cred)
			c.tokens.mu.Lock()
			c.tokens.creds = &creds
			c.tokens.mu.Unlock()
			return nil
		}
	}
	c.tokens.mu.Lock()
	hasMemCreds := c.tokens.creds != nil && c.tokens.creds.AccessToken != ""
	c.tokens.mu.Unlock()
	if hasMemCreds {
		return nil
	}
	return fmt.Errorf("codex-oauth: no credentials found")
}

// Connect validates OAuth tokens, then persists them — same validation-window
// pattern as ClaudeOAuth.Connect (tokens.validating blocks disk syncs from
// stomping these just-obtained creds mid-FetchModels).
func (c *CodexOAuth) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeOAuth {
		return fmt.Errorf("codex-oauth expects oauth credentials, got %s", creds.Type)
	}
	if creds.AccessToken == "" || creds.RefreshToken == "" {
		return fmt.Errorf("access_token and refresh_token cannot be empty")
	}
	// The Codex backend requires ChatGPT-Account-ID on every request. It is
	// extracted from the login flow's id_token; an empty one means the flow
	// couldn't find it — fail loudly at connect time, not at first request.
	if creds.AccountID == "" {
		return fmt.Errorf("account_id cannot be empty (extracted from the login's id_token)")
	}

	c.tokens.mu.Lock()
	c.tokens.creds = &creds
	c.tokens.validating = true
	c.tokens.mu.Unlock()

	if _, err := c.FetchModels(); err != nil {
		c.tokens.mu.Lock()
		c.tokens.creds = nil
		c.tokens.validating = false
		c.tokens.mu.Unlock()
		if strings.Contains(err.Error(), "session expired") {
			return err
		}
		return fmt.Errorf("invalid credentials: %w", err)
	}

	persistCodexCreds(&creds)
	c.tokens.mu.Lock()
	c.tokens.validating = false
	c.tokens.mu.Unlock()
	return nil
}

func (c *CodexOAuth) Disconnect() error { return c.clearCreds() }

func (c *CodexOAuth) clearCreds() error {
	c.tokens.creds = nil
	c.mu.Lock()
	c.cache = make(map[string]types.ModelMeta)
	c.mu.Unlock()
	return config.GetCredentialsManager().DeleteCredential("codex-oauth")
}

// ── Model cache ──────────────────────────────────────────────────────────

func (c *CodexOAuth) Models() []types.ModelMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(c.cache))
	for _, m := range c.cache {
		out = append(out, m)
	}
	return out
}

func (c *CodexOAuth) ModelMeta(modelID string) *types.ModelMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (c *CodexOAuth) FetchModels() ([]types.ModelMeta, error) {
	creds, err := c.ResolveCredentials()
	if err != nil {
		return nil, fmt.Errorf("oauth token invalid: %w", err)
	}
	metas, err := fetchCodexModels(creds)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		c.cache[m.ID] = m
	}
	c.mu.Unlock()
	return metas, nil
}

// ── Streaming ────────────────────────────────────────────────────────────

func (c *CodexOAuth) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	creds, err := c.ResolveCredentials()
	if err != nil {
		return nil, fmt.Errorf("oauth token: %w", err)
	}

	codexReq, err := buildCodexRequest(req, c.session)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(codexReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", codexResponsesURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	httpReq.Header.Set("originator", codexOriginator)
	httpReq.Header.Set("User-Agent", codexUserAgent())
	// Header name is "session-id" (hyphen), NOT "session_id" — verified
	// against Codex CLI's own codex-api/src/requests/headers.rs
	// (build_session_headers inserts literal "session-id"/"thread-id").
	// This is the header the backend's prompt-cache router actually keys
	// on; prompt_cache_key in the body is the routing HINT, but shipping
	// the wrong header name here meant every request could land on a
	// different backend worker with no warm cache to write against —
	// the likely root cause of cache_write_tokens reading 0 across an
	// entire live session (investigated 2026-09-10).
	httpReq.Header.Set("session-id", c.session)
	if creds.AccountID != "" {
		httpReq.Header.Set("ChatGPT-Account-ID", creds.AccountID)
	}

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	// 401 = token expired/revoked. auth.openai.com rotates refresh tokens on
	// every refresh, and the tokenManager's own RMW path already handles
	// refresh; a 401 after ResolveCredentials (which just refreshed) means
	// the credential is genuinely dead — surface a re-auth message.
	if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("codex-oauth session expired (run 'harness connect codex-oauth' to re-authenticate): HTTP %d: %s", httpResp.StatusCode, string(b))
	}
	if httpResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, types.NewProviderAPIError("codex-oauth", httpResp.StatusCode, b)
	}

	resp, err := parseCodexStream(ctx, httpResp.Body, cb)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ── Request translation: internal format → Codex Responses dialect ───────

// codexToolStreamState tracks one in-flight function_call announced by
// output_item.added — its backend item id (the key), the canonical harness
// tool id (so Start/Delta/End all carry the SAME id and the TUI's block
// pairing works), the function name, and the accumulated argument deltas.
type codexToolStreamState struct {
	callID  string
	name    string
	argsBuf strings.Builder
}

// canonicalID maps the backend's call_id into the harness-canonical
// "toolu_<24chars>" form — the same multi-provider standard every parser
// feeds the store (parseOpenAIStream hashes "call_xxx" IDs through the same
// llm.ToolIDFor; Anthropic's native toolu_ IDs pass through unchanged), so
// a session's persisted tool IDs stay resume-compatible across providers.
// Deterministic: same call_id → same canonical id.
func (s *codexToolStreamState) canonicalID() string {
	return llm.ToolIDFor(s.callID)
}

// codexRequest is the wire format chatgpt.com/backend-api/codex/responses
// expects — the Responses API dialect, NOT Chat Completions (api.openai.com).
// Field-by-field notes are per-target research-verified requirements, not
// style choices:
//   - store: false is MANDATORY — the backend rejects store:true outright
//     (HTTP 400 "Store must be set to false"), and that in turn is why
//     previous_response_id chaining is impossible: the whole conversation is
//     resent every turn, and the server's prompt cache (routed by the
//     session_id header + prompt_cache_key) is what makes that affordable.
//   - max_output_tokens is OMITTED — the backend rejects it as an unsupported
//     parameter, unlike the public Responses API (Zed comment verbatim). The
//     AgentOptions.MaxTokens value is deliberately dropped for this provider.
//   - system messages are HOISTED to the top-level instructions field
//     (joined "\n\n") rather than sent as input items.
//   - tools are FLAT (name/parameters at top level, no "function" nesting).
type codexRequest struct {
	Model             string           `json:"model"`
	Instructions      string           `json:"instructions,omitempty"`
	Input             []codexInputItem `json:"input"`
	Tools             []codexTool      `json:"tools,omitempty"`
	ToolChoice        string           `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Stream            bool             `json:"stream"`
	Store             bool             `json:"store"`
	Include           []string         `json:"include,omitempty"`
	PromptCacheKey    string           `json:"prompt_cache_key,omitempty"`
	Reasoning         *codexReasoning  `json:"reasoning,omitempty"`
}

type codexReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type codexTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// codexInputItem is one input item — the Responses API's tagged union
// ({"type": ...}). Only the variants harness's conversation shape produces:
// messages (user text/image, assistant text), function_call /
// function_call_output pairs, and reasoning replay items.
// Encoding via marshalJSON with manual type tagging (Go's encoding/json has
// no tagged enums; a single struct with an explicit Type field set per
// variant keeps the wire shape explicit).
type codexInputItem struct {
	Type string `json:"type"` // "message" | "function_call" | "function_call_output" | "reasoning"

	// message variant
	Role    string         `json:"role,omitempty"`
	Content []codexContent `json:"content,omitempty"`

	// function_call variant
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`

	// function_call_output variant
	Output string `json:"output,omitempty"`

	// reasoning variant — summary MUST be present ON REASONING ITEMS (an
	// empty [] passes; the field being OMITTED there is rejected: 400
	// missing_required_parameter) but must NOT appear on ANY other item
	// shape (400 unknown_parameter 'input[N].summary' — the fat-struct bug
	// Gus hit: a plain message item serialized "summary":null). Pointer +
	// nil = omitted; reasoning items set &[].
	ID               string            `json:"id,omitempty"`
	EncryptedContent string            `json:"encrypted_content,omitempty"`
	Summary          *[]map[string]any `json:"summary,omitempty"`
}

type codexContent struct {
	Type string `json:"type"` // "input_text" | "input_image" | "output_text"
	Text string `json:"text,omitempty"`
	// ImageURL for input_image
	ImageURL string `json:"image_url,omitempty"`
}

func codexUserAgent() string {
	return fmt.Sprintf("codex_cli_rs/%s (external, cli)", codexVersion)
}

func buildCodexRequest(req *types.Request, sessionID string) (*codexRequest, error) {
	// System prompt → top-level instructions (the backend requires this).
	instructions := req.SystemPrompt

	// Reasoning effort from thinking level. Codex has no xhigh — clamp to
	// high. "off" omits the field entirely (the backend picks the model's
	// own default_reasoning_level).
	var reasoning *codexReasoning
	switch req.ThinkingLevel {
	case "low", "medium", "high":
		reasoning = &codexReasoning{Effort: req.ThinkingLevel}
	case "xhigh":
		reasoning = &codexReasoning{Effort: "high"}
	}

	input, err := buildCodexInput(req.Messages)
	if err != nil {
		return nil, err
	}

	out := &codexRequest{
		Model:        req.Model,
		Instructions: instructions,
		Input:        input,
		Stream:       true,
		Store:        false,
		Include:      []string{"reasoning.encrypted_content"},
		// prompt_cache_key = the conversation's stable session id, exactly
		// as Codex CLI does — it's the server-side prompt-cache routing key.
		PromptCacheKey: sessionID,
		Reasoning:      reasoning,
	}
	if req.SystemPrompt == "" {
		out.Instructions = ""
	}

	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, codexTool{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
		auto := "auto"
		out.ToolChoice = auto
		parallel := true
		out.ParallelToolCalls = &parallel
	}
	return out, nil
}

// buildCodexInput translates harness's provider-agnostic messages into the
// Responses API's tagged input items. Tool-call pairing is keyed by the
// SAME ids the provider assigned (call_id on function_call, call_id on
// function_call_output) — no re-keying needed, unlike the Anthropic mapping.
func buildCodexInput(messages []types.Message) ([]codexInputItem, error) {
	var items []codexInputItem
	for _, m := range messages {
		for _, p := range m.Parts {
			switch {
			case p.ToolResult != nil:
				items = append(items, codexInputItem{
					Type:   "function_call_output",
					CallID: p.ToolResult.ID,
					Output: p.ToolResult.Output,
				})
			case p.ToolCall != nil:
				args := string(p.ToolCall.Input)
				items = append(items, codexInputItem{
					Type:      "function_call",
					Name:      p.ToolCall.Name,
					Arguments: args,
					CallID:    p.ToolCall.ID,
				})
			case p.Thinking != nil:
				// Replay ONLY when we hold the backend's own opaque pair —
				// the encrypted_content (Signature) AND the item id it was
				// issued under (OpenAIItemID). The backend verifies the
				// payload is bound to that exact id: replaying it under a
				// synthetic id 400s "Encrypted content item_id did not match
				// the target item id" (verified live). Other verified shapes:
				//   - empty encrypted_content + summary:[]                → 400
				//     unknown_parameter 'input[N].summary' (Gus's original
				//     error — an empty-content item is a DIFFERENT shape where
				//     summary isn't a field at all)
				//   - absent summary (with encrypted_content present)     → 400
				//     missing_required_parameter
				// So: no real token pair → DROP the reasoning item entirely
				// (the conversation replays without it; the backend re-reasons
				// from context, exactly what any non-encrypted provider does).
				// It's a prompt-cache hint, not a correctness requirement.
				if p.Thinking.Signature != "" && p.Thinking.OpenAIItemID != "" {
					emptySummary := []map[string]any{}
					items = append(items, codexInputItem{
						Type:             "reasoning",
						ID:               p.Thinking.OpenAIItemID,
						EncryptedContent: p.Thinking.Signature,
						Summary:          &emptySummary, // MUST be present ([]), pointer so other shapes omit it
					})
				}
			case p.Image != nil:
				items = append(items, codexInputItem{
					Type: "message", Role: "user",
					Content: []codexContent{{Type: "input_image", ImageURL: "data:" + p.Image.MimeType + ";base64," + p.Image.Base64}},
				})
			case p.Text != "":
				role := "user"
				if m.Role == types.RoleAssistant {
					role = "assistant"
					items = append(items, codexInputItem{
						Type: "message", Role: role,
						Content: []codexContent{{Type: "output_text", Text: p.Text}},
					})
					continue
				}
				items = append(items, codexInputItem{
					Type: "message", Role: role,
					Content: []codexContent{{Type: "input_text", Text: p.Text}},
				})
			}
		}
	}
	return items, nil
}

// ── SSE parsing — the Codex Responses dialect ────────────────────────────

// parseCodexStream parses chatgpt.com's Responses SSE dialect. Deliberately
// NOT parseOpenAIStream — api.openai.com's chunk shape (choices[].delta) is
// entirely different from this event stream (response.* typed events).
// Same dropped-connection hardening contract as v0.76.53: the channel
// closing without response.completed is an error, not success.
func parseCodexStream(ctx context.Context, body io.Reader, cb types.StreamCallback) (*types.Response, error) {
	emit := func(e types.StreamEvent) {
		if cb != nil {
			cb(e)
		}
	}

	resp := &types.Response{}
	var textBuf, reasoningBuf string
	var lastReasoningID, lastReasoningEncContent string
	// Accumulate tool calls for resp.ToolCalls — the ReAct loop executes
	// tools from the RETURNED response, not from the event stream (events
	// are the transports' rendering feed; see parseOpenAIStream doing the
	// same). Missing this made a function_call turn persist an empty
	// assistant message and execute nothing — "the model said nothing".
	var pendingTools []types.ToolCall

	sseCh, sseErr := llm.ParseSSE(ctx, body)
	sawCompleted := false

	// Tool-call streaming state — the Codex dialect announces a function_call
	// via output_item.added (id/call_id/name, empty arguments), streams its
	// arguments via response.function_call_arguments.delta, and finalizes it
	// via output_item.done. All three map onto harness's own three-phase
	// tool event flow (StreamToolStart → StreamToolDelta → StreamToolEnd):
	// without ToolStart/ToolDelta the transports never render the tool
	// header/streaming args while the call executes (observed live: the TUI
	// showed nothing during the entire Bash/web_search execution).
	toolStates := map[string]*codexToolStreamState{} // keyed by output_item id

	for sse := range sseCh {
		var event struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Delta    string          `json:"delta"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(sse.Data), &event); err != nil {
			continue // non-JSON line (comment/keepalive) — skip
		}
		switch event.Type {
		case "response.output_text.delta":
			textBuf += event.Delta
			emit(types.StreamEvent{Type: types.StreamTextDelta, Delta: event.Delta})
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			reasoningBuf += event.Delta
			emit(types.StreamEvent{Type: types.StreamThinkingDelta, Delta: event.Delta})
		case "response.output_item.added":
			var item struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			}
			if err := json.Unmarshal(event.Item, &item); err != nil || item.Type != "function_call" {
				continue
			}
			st := &codexToolStreamState{
				callID:  item.CallID,
				name:    item.Name,
				argsBuf: strings.Builder{},
			}
			toolStates[item.ID] = st
			emit(types.StreamEvent{Type: types.StreamToolStart, ToolID: st.canonicalID(), ToolName: st.name})
		case "response.function_call_arguments.delta":
			// The delta event carries the item id (not call_id) — the same
			// key output_item.added registered.
			var d struct {
				ItemID string `json:"item_id"`
				Delta  string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(sse.Data), &d); err != nil {
				continue
			}
			st, ok := toolStates[d.ItemID]
			if !ok || d.Delta == "" {
				continue
			}
			st.argsBuf.WriteString(d.Delta)
			emit(types.StreamEvent{Type: types.StreamToolDelta, ToolID: st.canonicalID(), ToolName: st.name, Delta: d.Delta})
		case "response.output_item.done":
			var item struct {
				Type       string `json:"type"`
				ID         string `json:"id"`
				CallID     string `json:"call_id"`
				Name       string `json:"name"`
				Arguments  string `json:"arguments"`
				EncContent string `json:"encrypted_content"`
			}
			if err := json.Unmarshal(event.Item, &item); err != nil {
				continue
			}
			if item.Type == "reasoning" {
				// Capture the encrypted payload AND its item id for the NEXT
				// turn's replay — the backend verifies the payload is
				// cryptographically bound to the id ("Encrypted content
				// item_id did not match the target item id" on mismatch),
				// so both must round-trip exactly as received.
				lastReasoningID = item.ID
				lastReasoningEncContent = item.EncContent
			}
			if item.Type == "function_call" {
				// Finalize the three-phase flow: reuse the state the added
				// event registered (its canonicalID — so Start/Delta/End all
				// carry the SAME tool id and the TUI's block pairing works),
				// but the FINAL arguments come from the done item (the
				// authoritative complete string).
				st, known := toolStates[item.ID]
				var canonicalID, name string
				if known {
					canonicalID = st.canonicalID()
					name = st.name
				} else {
					// Added event never arrived (defensive) — derive from the
					// done item alone.
					name = item.Name
					canonicalID = llm.ToolIDFor(item.CallID)
				}
				input := json.RawMessage(item.Arguments)
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				if !json.Valid(input) {
					if safe, err := json.Marshal(item.Arguments); err == nil {
						input = json.RawMessage(`{"` + types.RawWrapperKey + `":` + string(safe) + `}`)
					} else {
						input = json.RawMessage("{}")
					}
				}
				emit(types.StreamEvent{Type: types.StreamToolEnd, ToolID: canonicalID, ToolName: name, ToolArgs: input})
				pendingTools = append(pendingTools, types.ToolCall{
					ID: canonicalID, Name: name, Input: input,
				})
				delete(toolStates, item.ID)
			}
		case "response.completed", "response.failed":
			sawCompleted = true
			// Usage rides the completed response envelope.
			var envelope struct {
				Usage struct {
					InputTokens        int `json:"input_tokens"`
					OutputTokens       int `json:"output_tokens"`
					InputTokensDetails struct {
						CachedTokens     int `json:"cached_tokens"`
						CacheWriteTokens int `json:"cache_write_tokens"`
					} `json:"input_tokens_details"`
					OutputTokensDetails struct {
						ReasoningTokens int `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(event.Response, &envelope); err == nil {
				resp.Usage.InputTokens = envelope.Usage.InputTokens
				resp.Usage.OutputTokens = envelope.Usage.OutputTokens
				resp.Usage.CacheRead = envelope.Usage.InputTokensDetails.CachedTokens
				// cache_write_tokens rides right beside cached_tokens — the
				// Codex backend's name for what Anthropic calls
				// cache_creation_input_tokens (verified live: the field exists
				// on every response, 0 until the backend persists new prefix
				// content to the prompt cache under this prompt_cache_key).
				resp.Usage.CacheWrite = envelope.Usage.InputTokensDetails.CacheWriteTokens
			}
			if event.Type == "response.failed" {
				return nil, fmt.Errorf("codex response failed: %s", string(event.Response))
			}
		}
	}

	// Dropped-connection hardening (v0.76.53 contract): the channel closing
	// is not by itself proof of completion — errFn() reports a genuine read
	// error once drained, and !sawCompleted catches a silent server-side
	// truncation. Both turn into a real error through the same path any
	// other stream error already flows through.
	if err := sseErr(); err != nil {
		return nil, fmt.Errorf("stream connection lost: %w", err)
	}
	if !sawCompleted {
		return nil, fmt.Errorf("stream ended unexpectedly before completion (no response.completed marker received) — the connection likely dropped mid-response")
	}

	resp.Text = textBuf
	resp.ToolCalls = pendingTools
	// Build the persisted assistant message here (same shape parseOpenAIStream
	// uses) — thinking content rides the reasoningBuf, and the LAST reasoning
	// item's id + encrypted_content ride ThinkingPart.{OpenAIItemID,Signature}
	// (harness's own replay-token fields): the Codex backend verifies that
	// opaque payload ON the id it was issued for, so both must round-trip
	// exactly as received.
	resp.Message = types.NewAssistantToolCallMessageWithOpenAIReplay(textBuf, reasoningBuf, lastReasoningID, lastReasoningEncContent, resp.ToolCalls)
	emit(types.StreamEvent{
		Type: types.StreamUsage, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens, CacheRead: resp.Usage.CacheRead,
	})
	emit(types.StreamEvent{Type: types.StreamDone})
	return resp, nil
}

// ── Models ───────────────────────────────────────────────────────────────

func fetchCodexModels(creds types.Credentials) ([]types.ModelMeta, error) {
	u := fmt.Sprintf("%s?client_version=%s", codexModelsURL, url.QueryEscape(codexVersion))
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("originator", codexOriginator)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexUserAgent())
	if creds.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", creds.AccountID)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("invalid or expired credentials (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, types.NewProviderAPIError("codex-oauth", resp.StatusCode, body)
	}

	// Cloudflare can return 200-with-HTML (a bot challenge) on this endpoint;
	// detecting that as a loud parse error (rather than caching garbage) is
	// deliberate — see brokk's notes on /models being the more aggressive
	// Cloudflare-gated endpoint.
	if !json.Valid(body) || body[0] != '{' {
		return nil, fmt.Errorf("codex models: non-JSON response (likely a Cloudflare challenge) — retry shortly")
	}
	var result struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			// The Codex endpoint is authoritative for context capacity. Keep
			// this ahead of EnrichMeta: OpenRouter is only a fallback for
			// fields Codex does not provide, and may describe a different
			// deployment/configuration of the same model.
			ContextWindow            int    `json:"context_window"`
			Visibility               string `json:"visibility"`
			Priority                 int    `json:"priority"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			SupportedReasoningLevels []struct {
				Effort      string `json:"effort"`
				Description string `json:"description"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse codex models response: %w", err)
	}

	// Keep only "list" (the picker-visible set) — "hide" entries are callable
	// but not shown, "none" is internal-only. Sort by priority descending
	// (higher first), matching the Codex CLI's own picker ordering.
	var visible []struct {
		Slug          string
		DisplayName   string
		Visibility    string
		Priority      int
		ContextWindow int
	}
	for _, m := range result.Models {
		if m.Visibility == "list" {
			visible = append(visible, struct {
				Slug          string
				DisplayName   string
				Visibility    string
				Priority      int
				ContextWindow int
			}{m.Slug, m.DisplayName, m.Visibility, m.Priority, m.ContextWindow})
		}
	}
	// priority desc — simple insertion sort over a tiny list (≤ ~15 models),
	// no sort import needed for one comparison.
	for i := 1; i < len(visible); i++ {
		for j := i; j > 0 && visible[j].Priority > visible[j-1].Priority; j-- {
			visible[j], visible[j-1] = visible[j-1], visible[j]
		}
	}

	var metas []types.ModelMeta
	for _, m := range visible {
		meta := llm.EnrichMeta(types.ModelMeta{
			ID:            m.Slug,
			DisplayName:   m.DisplayName,
			ContextWindow: m.ContextWindow,
			// Codex exposes context capacity here, not a generic output-token
			// limit; leave MaxTokens empty so EnrichMeta supplies its fallback.
			MaxTokens: 0,
		})
		metas = append(metas, meta)
	}
	return metas, nil
}

// ── Credential management internals ─────────────────────────────────────

// codexTokenManager mirrors claude-oauth's tokenManager: disk is the source
// of truth (credentials.json, "codex-oauth" key), refreshed via the
// filelock-hardened UpdateCredential RMW path, with an explicit validation
// window during Connect(). Refresh token ROTATION applies here too —
// auth.openai.com issues a fresh refresh_token on every refresh and
// invalidates the old one, so the single-endpoint/no-double-redeem rule is
// just as load-bearing here as it is for Claude.
type codexTokenManager struct {
	mu         sync.Mutex
	creds      *types.Credentials
	validating bool
}

func newCodexTokenManager() *codexTokenManager {
	tm := &codexTokenManager{}
	if cred, ok := config.GetCredentialsManager().Credential("codex-oauth"); ok && cred.AccessToken != "" {
		creds := codexCredFromDisk(cred)
		tm.creds = &creds
	}
	return tm
}

func (tm *codexTokenManager) getValidToken() (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.creds == nil {
		return "", fmt.Errorf("not connected")
	}
	if !tm.validating {
		tm.syncFromDisk()
	}
	if tm.creds.ExpiresAt > time.Now().UnixMilli()+expiryBufferMs {
		return tm.creds.AccessToken, nil
	}

	const maxAttempts = 3
	backoff := time.Second
	var accessToken string
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
		}
		permanent := false
		updateErr := config.GetCredentialsManager().UpdateCredential("codex-oauth",
			func(cur config.ProviderCredential, ok bool) (config.ProviderCredential, bool, error) {
				if !tm.validating && ok && cur.ExpiresAt > time.Now().UnixMilli()+expiryBufferMs {
					accessToken = cur.AccessToken
					synced := codexCredFromDisk(cur)
					tm.creds = &synced
					return cur, false, nil
				}
				refreshed, err := tm.refresh(tm.creds.RefreshToken)
				if err != nil {
					lastErr = err
					permanent = isAuthError(err)
					return cur, false, err
				}
				tm.creds = refreshed
				accessToken = refreshed.AccessToken
				return oauthProviderCredential(refreshed), true, nil
			})
		if updateErr != nil && !permanent {
			return "", fmt.Errorf("token refresh: %w", updateErr)
		}
		if accessToken != "" {
			return accessToken, nil
		}
		if permanent {
			break
		}
	}
	return "", fmt.Errorf("token refresh failed (run 'harness connect codex-oauth' to re-authenticate): %w", lastErr)
}

func (tm *codexTokenManager) syncFromDisk() {
	cred, ok := config.GetCredentialsManager().Credential("codex-oauth")
	if !ok || cred.AccessToken == "" {
		return
	}
	synced := codexCredFromDisk(cred)
	tm.creds = &synced
}

func (tm *codexTokenManager) refresh(refreshToken string) (*types.Credentials, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", codexClientID)
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := codexRefreshClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request (%s): %w", codexTokenURL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh HTTP %d (%s): %s", resp.StatusCode, codexTokenURL, string(body))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.AccessToken == "" {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	expiresIn := result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = codexDefaultExpiresIn
	}
	newRefresh := result.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}
	// The refresh response may carry a fresh id_token whose account id is the
	// authoritative one — re-extract (same fallback chain as login), keeping
	// the stored value only as a fallback if the new token omits it.
	accountID := oauthflow.ExtractChatGPTAccountID(result.IDToken)
	if accountID == "" {
		accountID = tm.creds.AccountID
	}
	creds := types.OAuthCredentialsWithAccount(
		result.AccessToken,
		newRefresh,
		time.Now().UnixMilli()+int64(expiresIn)*1000,
		tm.creds.SubscriptionType,
		accountID,
	)
	return &creds, nil
}

func codexCredFromDisk(cred config.ProviderCredential) types.Credentials {
	return types.OAuthCredentialsWithAccount(cred.AccessToken, cred.RefreshToken, cred.ExpiresAt, cred.SubscriptionType, cred.AccountID)
}

func persistCodexCreds(creds *types.Credentials) {
	_ = config.GetCredentialsManager().SetCredential("codex-oauth", codexProviderCredential(creds))
}

func codexProviderCredential(creds *types.Credentials) config.ProviderCredential {
	return config.ProviderCredential{
		Type:             "oauth",
		AccessToken:      creds.AccessToken,
		RefreshToken:     creds.RefreshToken,
		ExpiresAt:        creds.ExpiresAt,
		SubscriptionType: creds.SubscriptionType,
		AccountID:        creds.AccountID,
	}
}
