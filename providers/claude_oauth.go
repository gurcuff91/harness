package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// ── Constants ────────────────────────────────────────────────────────────

const (
	anthropicOAuthAPI = "https://api.anthropic.com/v1/messages"
	oauthTokenURL     = "https://claude.ai/v1/oauth/token"
	oauthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	expiryBufferMs    = 60_000

	// Claude Code stealth identity
	billingSalt = "59cf53e54c78"
	mcpPrefix   = "mcp__extensions__"
)

var ccVersion = envOrDefault("ANTHROPIC_CLI_VERSION", "2.1.90")

// ── ClaudeOAuth ──────────────────────────────────────────────────────────

// ClaudeOAuth implements Provider using Claude Code's OAuth subscription.
// Makes requests indistinguishable from Claude Code — uses your existing
// subscription instead of paying API rates.
type ClaudeOAuth struct {
	client  *http.Client
	tokens  *tokenManager // private — token lifecycle is internal
	session string        // stable session ID per harness instance
	cache   map[string]types.ModelMeta
	mu      sync.RWMutex
}

func NewClaudeOAuth() (*ClaudeOAuth, error) {
	c := &ClaudeOAuth{
		client:  &http.Client{Timeout: 5 * time.Minute},
		tokens:  newTokenManager(),
		session: uuid.New().String(),
		cache:   make(map[string]types.ModelMeta),
	}
	return c, nil
}

func (c *ClaudeOAuth) Name() string { return "claude-oauth" }

func (c *ClaudeOAuth) ActivationSource() ActivationSource {
	if _, err := c.ResolveCredentials(); err == nil {
		return ActivationCredentials
	}
	return ActivationNone
}
func (c *ClaudeOAuth) IsActive() bool {
	_, err := c.ResolveCredentials()
	return err == nil
}

func (c *ClaudeOAuth) CredentialType() types.CredentialType { return types.CredTypeOAuth }

// ── Credential management ────────────────────────────────────────────────

// ResolveCredentials reads from the credential chain AND validates the token.
// Chain: cache → credentials.json.
// Refreshes expired tokens automatically.
// Returns error if no credentials found or refresh fails (revoked).
func (c *ClaudeOAuth) ResolveCredentials() (types.Credentials, error) {
	if err := c.loadCredentialsFromSources(); err != nil {
		return types.Credentials{}, err
	}
	tok, err := c.tokens.getValidToken()
	if err != nil {
		return types.Credentials{}, fmt.Errorf("claude-oauth credentials invalid or expired: %w", err)
	}
	creds := *c.tokens.creds
	creds.AccessToken = tok
	return creds, nil
}

// loadCredentialsFromSources populates tokens.creds from:
// memory cache → credentials.json. Keychain is only read during /connect.
func (c *ClaudeOAuth) loadCredentialsFromSources() error {
	if c.tokens.creds != nil && c.tokens.creds.AccessToken != "" {
		return nil // already cached
	}
	cm := config.GetCredentialsManager()
	if at, ok := cm.Load(oauthCredPrefix + "access_token"); ok && at != "" {
		rt, _ := cm.Load(oauthCredPrefix + "refresh_token")
		var ea int64
		if eas, ok := cm.Load(oauthCredPrefix + "expires_at"); ok {
			fmt.Sscanf(eas, "%d", &ea)
		}
		st, _ := cm.Load(oauthCredPrefix + "subscription_type")
		creds := types.OAuthCredentials(at, rt, ea, st)
		c.tokens.creds = &creds
		return nil
	}
	return fmt.Errorf("claude-oauth: no credentials found")
}

// Connect validates OAuth tokens, then persists them.
func (c *ClaudeOAuth) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeOAuth {
		return fmt.Errorf("claude-oauth expects oauth credentials, got %s", creds.Type)
	}
	if creds.AccessToken == "" {
		return fmt.Errorf("access_token cannot be empty")
	}
	if creds.RefreshToken == "" {
		return fmt.Errorf("refresh_token cannot be empty")
	}

	// Validate first (in-memory only)
	c.tokens.creds = &creds
	if _, err := c.FetchModels(); err != nil {
		c.tokens.creds = nil
		return fmt.Errorf("invalid credentials: %w", err)
	}

	// Persist only after validation
	persistOAuthCreds(&creds)
	return nil
}

func (c *ClaudeOAuth) Disconnect() error { return c.clearCreds() }

func (c *ClaudeOAuth) clearCreds() error {
	c.tokens.creds = nil
	c.mu.Lock()
	c.cache = make(map[string]types.ModelMeta)
	c.mu.Unlock()
	return config.GetCredentialsManager().DeletePrefix(oauthCredPrefix)
}

// ── Model cache ──────────────────────────────────────────────────────────

func (c *ClaudeOAuth) Models() []types.ModelMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(c.cache))
	for _, m := range c.cache {
		out = append(out, m)
	}
	return out
}

func (c *ClaudeOAuth) ModelMeta(modelID string) *types.ModelMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (c *ClaudeOAuth) FetchModels() ([]types.ModelMeta, error) {
	tok, err := c.tokens.getValidToken()
	if err != nil {
		return nil, fmt.Errorf("oauth token invalid: %w", err)
	}
	metas, err := fetchAnthropicModels(tok)
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

func (c *ClaudeOAuth) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	// Get token under lock, release before HTTP call
	c.mu.Lock()
	token, err := c.tokens.getValidToken()
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("oauth token: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("oauth: empty token")
	}

	// Pre-resolve thinking from ModelMeta (authoritative)
	thinkingFull := llm.BuildAnthropicThinkingFromMeta(c.ModelMeta(req.Model), req.ThinkingLevel, req.MaxTokens)

	// Pre-build wire messages (OAuth: drop non-redacted thinking + cache_control)
	wireMsgs := buildWireMessages(req.Messages)

	// CC identity headers (billing header needs wire messages)
	billingHeader := buildBillingHeader(wireMsgs)
	headers := buildCCHeaders(token, billingHeader, c.session)

	// Interleaved-thinking beta only for non-adaptive models
	if req.ThinkingLevel != "" && req.ThinkingLevel != "off" && thinkingFull.OutputConfig == nil {
		headers["anthropic-beta"] += ",interleaved-thinking-2025-05-14"
	}

	anthrReq := &llm.AnthropicRequest{
		Request:        req,
		ThinkingConfig: &thinkingFull,
		WireMessages:   wireMsgs,
		SystemBlocks:   buildSystemBlocks(req.SystemPrompt),
		Tools:          buildOAuthTools(req.Tools),
		UnmapTool:      unmapToolNameFromCC,
	}

	return llm.DoAnthropicStream(ctx, c.client, anthropicOAuthAPI, token, anthrReq, headers, cb)
}

// ── Request builders ─────────────────────────────────────────────────────

var ephemeralCC = &llm.AnthropicCacheControl{Type: "ephemeral"}

func buildOAuthTools(defs []types.ToolDef) []llm.AnthropicTool {
	tools := make([]llm.AnthropicTool, len(defs))
	for i, t := range defs {
		tools[i] = llm.AnthropicTool{
			Name:                mapToolNameToCC(t.Name),
			Description:         t.Description,
			InputSchema:         t.InputSchema,
			EagerInputStreaming: true,
		}
	}
	if len(tools) > 0 {
		tools[len(tools)-1].CacheControl = ephemeralCC
	}
	return tools
}

func buildSystemBlocks(systemPrompt string) []map[string]any {
	blocks := []map[string]any{{
		"type":          "text",
		"text":          "You are Claude Code, Anthropic's official CLI for Claude.",
		"cache_control": map[string]string{"type": "ephemeral"},
	}}
	if systemPrompt != "" {
		blocks = append(blocks, map[string]any{
			"type":          "text",
			"text":          systemPrompt,
			"cache_control": map[string]string{"type": "ephemeral"},
		})
	}
	return blocks
}

func buildWireMessages(messages []types.Message) []json.RawMessage {
	wire := make([]json.RawMessage, 0, len(messages))
	for _, m := range messages {
		for _, w := range translateMessageOAuth(m) {
			wire = append(wire, w)
		}
	}
	return addLastUserCacheControl(wire)
}

// translateMessageOAuth converts messages for OAuth:
// drops non-redacted thinking (saves ~1-2K tokens/turn), keeps only signatures.
func translateMessageOAuth(msg types.Message) []json.RawMessage {
	switch msg.Role {
	case types.RoleUser:
		return llm.TranslateMessageToAnthropic(msg)
	case types.RoleAssistant:
		var content []map[string]any
		for _, p := range msg.Parts {
			switch {
			case p.Thinking != nil:
				// Only redacted thinking (signature, zero token cost).
				// Non-redacted dropped — unbounded token growth otherwise.
				if p.Thinking.Signature != "" && p.Thinking.Content == "" {
					content = append(content, map[string]any{
						"type": "redacted_thinking",
						"data": p.Thinking.Signature,
					})
				}
			case p.Text != "":
				content = append(content, map[string]any{"type": "text", "text": p.Text})
			case p.ToolCall != nil:
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    p.ToolCall.ID,
					"name":  mapToolNameToCC(p.ToolCall.Name),
					"input": json.RawMessage(p.ToolCall.Input),
				})
			}
		}
		if len(content) == 0 {
			return nil
		}
		d, _ := json.Marshal(map[string]any{"role": "assistant", "content": content})
		return []json.RawMessage{d}
	}
	return nil
}

// addLastUserCacheControl marks the last text/tool_result in the last user
// message for prompt caching (~90% cost reduction on cache hits).
func addLastUserCacheControl(msgs []json.RawMessage) []json.RawMessage {
	for i := len(msgs) - 1; i >= 0; i-- {
		var msg map[string]any
		if err := json.Unmarshal(msgs[i], &msg); err != nil || msg["role"] != "user" {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			msg["content"] = []any{map[string]any{
				"type": "text", "text": msg["content"],
				"cache_control": map[string]string{"type": "ephemeral"},
			}}
		} else {
			for j := len(content) - 1; j >= 0; j-- {
				block, ok := content[j].(map[string]any)
				if !ok {
					continue
				}
				t, _ := block["type"].(string)
				if t == "text" || t == "tool_result" {
					block["cache_control"] = map[string]string{"type": "ephemeral"}
					content[j] = block
					break
				}
			}
			msg["content"] = content
		}
		data, _ := json.Marshal(msg)
		msgs[i] = data
		return msgs
	}
	return msgs
}

// ── Claude Code stealth identity ─────────────────────────────────────────

// CC-native tools — maps harness tool names to Claude Code canonical names.
// Our built-in tools that have a CC equivalent map directly (no MCP prefix).
// Any future tool not in this set gets mcp__extensions__ prefix automatically.
var ccToolNames = map[string]string{
	// Harness built-ins → CC canonical
	"bash": "Bash", "read": "Read", "write": "Write",
	"edit": "Edit", "fetch": "WebFetch", "skill": "Skill",
	// CC originals (pass-through)
	"grep": "Grep", "glob": "Glob", "askuserquestion": "AskUserQuestion",
	"enterplanmode": "EnterPlanMode", "exitplanmode": "ExitPlanMode",
	"killshell": "KillShell", "notebookedit": "NotebookEdit",
	"task": "Task", "taskoutput": "TaskOutput",
	"todowrite": "TodoWrite", "webfetch": "WebFetch", "websearch": "WebSearch",
}

func mapToolNameToCC(name string) string {
	if cc, ok := ccToolNames[strings.ToLower(name)]; ok {
		return cc
	}
	return mcpPrefix + name
}

// harnessToolNames maps CC canonical names → actual harness tool names (inbound).
// Explicit map needed because ccToolNames uses lowercase keys but harness names
// are capitalized (e.g. "fetch"→"WebFetch" outbound, "WebFetch"→"Fetch" inbound).
var harnessToolNames = map[string]string{
	"Bash": "Bash", "Read": "Read", "Write": "Write",
	"Edit": "Edit", "WebFetch": "Fetch", "Skill": "Skill",
}

func unmapToolNameFromCC(name string) string {
	if strings.HasPrefix(name, mcpPrefix) {
		return strings.TrimPrefix(name, mcpPrefix)
	}
	if harness, ok := harnessToolNames[name]; ok {
		return harness
	}
	return name
}

// buildCCHeaders returns Claude Code identity headers as a map.
func buildCCHeaders(token, billingHeader, sessionID string) map[string]string {
	headers := map[string]string{
		"Authorization":     "Bearer " + token,
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "claude-code-20250219,oauth-2025-04-20",
		"anthropic-dangerous-direct-browser-access": "true",
		"x-app":                    "cli",
		"user-agent":               "claude-cli/" + ccVersion + " (external, cli)",
		"x-client-request-id":      uuid.New().String(),
		"X-Claude-Code-Session-Id": sessionID,
	}
	if billingHeader != "" {
		headers["x-anthropic-billing-header"] = billingHeader
	}
	return headers
}

// setCCHeaders sets Claude Code identity headers on an http.Request (kept for compatibility).
func setCCHeaders(req *http.Request, token, billingHeader, sessionID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	req.Header.Set("x-app", "cli")
	req.Header.Set("user-agent", "claude-cli/"+ccVersion+" (external, cli)")
	req.Header.Set("x-client-request-id", uuid.New().String())
	req.Header.Set("X-Claude-Code-Session-Id", sessionID)
	if billingHeader != "" {
		req.Header.Set("x-anthropic-billing-header", billingHeader)
	}
}

func buildBillingHeader(messages []json.RawMessage) string {
	text := extractFirstUserText(messages)
	sampled := sampleChars(text, []int{4, 7, 20})
	suffix := sha256hex(billingSalt + sampled + ccVersion)[:3]
	cch := sha256hex(text)[:5]
	return fmt.Sprintf("cc_version=%s.%s; cc_entrypoint=cli; cch=%s;", ccVersion, suffix, cch)
}

func extractFirstUserText(messages []json.RawMessage) string {
	for _, raw := range messages {
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil || msg.Role != "user" {
			continue
		}
		switch v := msg.Content.(type) {
		case string:
			return v
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if m["type"] == "text" {
						if t, ok := m["text"].(string); ok {
							return t
						}
					}
				}
			}
		}
	}
	return ""
}

func sampleChars(text string, indices []int) string {
	runes := []rune(text)
	result := make([]rune, len(indices))
	for i, idx := range indices {
		if idx < len(runes) {
			result[i] = runes[idx]
		} else {
			result[i] = '0'
		}
	}
	return string(result)
}

func sha256hex(input string) string {
	h := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", h)
}

// ── tokenManager (private) ───────────────────────────────────────────────

// tokenManager handles the OAuth token lifecycle.
// Private — callers interact with ClaudeOAuth, not tokenManager directly.
type tokenManager struct {
	mu    sync.Mutex
	creds *types.Credentials
}

func newTokenManager() *tokenManager {
	tm := &tokenManager{}
	// Load from credentials.json at startup
	cm := config.GetCredentialsManager()
	at, _ := cm.Load(oauthCredPrefix + "access_token")
	if at == "" {
		return tm
	}
	rt, _ := cm.Load(oauthCredPrefix + "refresh_token")
	var ea int64
	if eas, ok := cm.Load(oauthCredPrefix + "expires_at"); ok {
		fmt.Sscanf(eas, "%d", &ea)
	}
	st, _ := cm.Load(oauthCredPrefix + "subscription_type")
	creds := types.OAuthCredentials(at, rt, ea, st)
	tm.creds = &creds
	return tm
}

// getValidToken returns a valid access token, refreshing if necessary.
func (tm *tokenManager) getValidToken() (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.creds == nil {
		return "", fmt.Errorf("not connected")
	}
	if tm.creds.ExpiresAt > time.Now().UnixMilli()+expiryBufferMs {
		return tm.creds.AccessToken, nil
	}
	refreshed, err := tm.refresh(tm.creds.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("token expired and refresh failed")
	}
	tm.creds = refreshed
	persistOAuthCreds(refreshed)
	return refreshed.AccessToken, nil
}

func (tm *tokenManager) refresh(refreshToken string) (*types.Credentials, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {oauthClientID},
		"refresh_token": {refreshToken},
	}
	resp, err := http.Post(oauthTokenURL, "application/x-www-form-urlencoded",
		bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.AccessToken == "" {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	expiresIn := result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 36000
	}
	newRefresh := result.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}
	return &types.Credentials{
		Type:             types.CredTypeOAuth,
		AccessToken:      result.AccessToken,
		RefreshToken:     newRefresh,
		ExpiresAt:        time.Now().UnixMilli() + int64(expiresIn)*1000,
		SubscriptionType: tm.creds.SubscriptionType,
	}, nil
}

// ── Credential persistence ───────────────────────────────────────────────

const oauthCredPrefix = "claude-oauth."

func persistOAuthCreds(creds *types.Credentials) {
	cm := config.GetCredentialsManager()
	cm.Store(oauthCredPrefix+"access_token", creds.AccessToken)
	cm.Store(oauthCredPrefix+"refresh_token", creds.RefreshToken)
	cm.Store(oauthCredPrefix+"expires_at", fmt.Sprintf("%d", creds.ExpiresAt))
	cm.Store(oauthCredPrefix+"subscription_type", creds.SubscriptionType)
}

// ── Utilities ────────────────────────────────────────────────────────────

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
