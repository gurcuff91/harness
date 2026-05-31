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
	"os/exec"
	"path/filepath"
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

// ClaudeOAuth implements llm.Provider using Claude Code's OAuth subscription.
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
	if c.IsActive() {
		c.FetchModels()
	}
	return c, nil
}

func (c *ClaudeOAuth) Name() string { return "claude-oauth" }

func (c *ClaudeOAuth) IsActive() bool {
	_, err := c.ResolveCredentials()
	return err == nil
}

func (c *ClaudeOAuth) CredentialType() types.CredentialType { return types.CredTypeOAuth }

// ── Credential management ────────────────────────────────────────────────

// ResolveCredentials reads from the credential chain AND validates the token.
// Chain: cache → credentials.json → keychain (bootstrap only).
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
// cache → credentials.json → keychain (bootstrap, persists on first load).
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
	// Keychain bootstrap — import and persist on first use
	if t := readClaudeFromKeychain(); t != nil {
		c.tokens.creds = t
		persistOAuthCreds(t)
		return nil
	}
	return fmt.Errorf("claude-oauth: no credentials found")
}

// SaveCredentials persists OAuth tokens and activates the provider.
func (c *ClaudeOAuth) SaveCredentials(creds types.Credentials) error {
	if creds.Type != types.CredTypeOAuth {
		return fmt.Errorf("claude-oauth expects oauth credentials, got %s", creds.Type)
	}
	if creds.AccessToken == "" {
		return fmt.Errorf("access_token cannot be empty")
	}
	c.tokens.creds = &creds
	persistOAuthCreds(&creds)
	c.FetchModels()
	return nil
}

func (c *ClaudeOAuth) ClearCredentials() error {
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

func (c *ClaudeOAuth) FetchModels() []types.ModelMeta {
	// Use the existing token — no new tokenManager needed
	tok, err := c.tokens.getValidToken()
	if err != nil {
		return nil
	}
	metas := fetchAnthropicModels(tok)
	c.mu.Lock()
	c.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		c.cache[m.ID] = m
	}
	c.mu.Unlock()
	return metas
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

	// Tools — MCP stealth naming + cache_control on last + eager streaming
	aTools := buildOAuthTools(req.Tools)

	// System blocks — stable for prompt caching
	systemBlocks := buildSystemBlocks(req.SystemPrompt)

	// Thinking config
	thinkingCfg, maxTokens := llm.BuildAnthropicThinking(req.Model, req.ThinkingLevel, req.MaxTokens)

	// Messages — drop non-redacted thinking, add cache_control on last user msg
	wireMsgs := buildWireMessages(req.Messages)

	body := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"system":     systemBlocks,
		"messages":   wireMsgs,
		"tools":      aTools,
		"stream":     true,
		"thinking":   thinkingCfg,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicOAuthAPI, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	billingHeader := buildBillingHeader(wireMsgs)
	setCCHeaders(httpReq, token, billingHeader, c.session)

	// Interleaved thinking for non-adaptive models
	if req.ThinkingLevel != "" && req.ThinkingLevel != "disable" && !llm.IsAdaptiveThinking(req.Model) {
		existing := httpReq.Header.Get("anthropic-beta")
		httpReq.Header.Set("anthropic-beta", existing+",interleaved-thinking-2025-05-14")
	}

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic OAuth API error %d: %s", httpResp.StatusCode, string(body))
	}

	return llm.ParseAnthropicStream(httpResp.Body, cb, unmapToolNameFromCC)
}

// ── Request builders ─────────────────────────────────────────────────────

type oauthTool struct {
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	InputSchema        json.RawMessage `json:"input_schema"`
	EagerInputStreaming bool            `json:"eager_input_streaming,omitempty"`
	CacheControl       *cacheCtrl      `json:"cache_control,omitempty"`
}

type cacheCtrl struct {
	Type string `json:"type"`
}

var ephemeralCC = &cacheCtrl{Type: "ephemeral"}

func buildOAuthTools(defs []types.ToolDef) []oauthTool {
	tools := make([]oauthTool, len(defs))
	for i, t := range defs {
		tools[i] = oauthTool{
			Name:               mapToolNameToCC(t.Name),
			Description:        t.Description,
			InputSchema:        t.InputSchema,
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

// CC-native tools keep canonical casing; everything else gets MCP prefix.
var ccToolNames = map[string]string{
	"read": "Read", "write": "Write", "edit": "Edit", "bash": "Bash",
	"grep": "Grep", "glob": "Glob", "askuserquestion": "AskUserQuestion",
	"enterplanmode": "EnterPlanMode", "exitplanmode": "ExitPlanMode",
	"killshell": "KillShell", "notebookedit": "NotebookEdit",
	"skill": "Skill", "task": "Task", "taskoutput": "TaskOutput",
	"todowrite": "TodoWrite", "webfetch": "WebFetch", "websearch": "WebSearch",
}

func mapToolNameToCC(name string) string {
	if cc, ok := ccToolNames[strings.ToLower(name)]; ok {
		return cc
	}
	return mcpPrefix + name
}

func unmapToolNameFromCC(name string) string {
	if strings.HasPrefix(name, mcpPrefix) {
		return strings.TrimPrefix(name, mcpPrefix)
	}
	for original, cc := range ccToolNames {
		if name == cc {
			return original
		}
	}
	return name
}

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
	suffix := sha256hex(billingSalt+sampled+ccVersion)[:3]
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
		return "", fmt.Errorf("not connected — use /connect claude-oauth")
	}
	if tm.creds.ExpiresAt > time.Now().UnixMilli()+expiryBufferMs {
		return tm.creds.AccessToken, nil
	}
	refreshed, err := tm.refresh(tm.creds.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("token expired — use /connect claude-oauth")
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

// ── OAuth login flow ─────────────────────────────────────────────────────

// RunOAuthFlow executes the OAuth login flow and returns credentials.
// Called by the CLI transport — UI interaction lives here, not in the provider.
func RunOAuthFlow() *types.Credentials {
	if err := login(); err != nil {
		return nil
	}
	// Re-read from credentials.json (login() just wrote them)
	cm := config.GetCredentialsManager()
	at, ok := cm.Load(oauthCredPrefix + "access_token")
	if !ok || at == "" {
		return nil
	}
	rt, _ := cm.Load(oauthCredPrefix + "refresh_token")
	var ea int64
	if eas, ok := cm.Load(oauthCredPrefix + "expires_at"); ok {
		fmt.Sscanf(eas, "%d", &ea)
	}
	st, _ := cm.Load(oauthCredPrefix + "subscription_type")
	creds := types.OAuthCredentials(at, rt, ea, st)
	return &creds
}

// login runs `claude auth login`, imports tokens, persists them.
func login() error {
	fmt.Println("\n  🔑 Starting authentication via Claude Code...")
	cmd := exec.Command("claude", "auth", "login")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		resetTerminal()
		return fmt.Errorf("claude auth login failed: %w\n  Install: npm install -g @anthropic-ai/claude-code", err)
	}
	resetTerminal()

	token := readClaudeFromKeychain()
	if token == nil {
		token = readClaudeCredentialsFile()
	}
	if token == nil {
		return fmt.Errorf("login completed but couldn't import tokens — try again")
	}
	persistOAuthCreds(token)
	return nil
}

func resetTerminal() {
	exec.Command("stty", "sane").Run()
	fmt.Print("\033[?25h\033[0m")
}

// ── Keychain readers (macOS) ─────────────────────────────────────────────

func readClaudeFromKeychain() *types.Credentials {
	if t := readKeychainItem("Claude Code-credentials"); t != nil {
		return t
	}
	return readKeychainItem("claude-code")
}

func readKeychainItem(service string) *types.Credentials {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil
	}
	data := raw
	if nested, ok := raw["claudeAiOauth"].(map[string]any); ok {
		data = nested
	}
	at, _ := data["accessToken"].(string)
	rt, _ := data["refreshToken"].(string)
	ea, _ := data["expiresAt"].(float64)
	st, _ := data["subscriptionType"].(string)
	if at == "" || rt == "" {
		return nil
	}
	return &types.Credentials{
		Type: types.CredTypeOAuth, AccessToken: at, RefreshToken: rt,
		ExpiresAt: int64(ea), SubscriptionType: st,
	}
}

func readClaudeCredentialsFile() *types.Credentials {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		return nil
	}
	var creds struct {
		OAuthTokens []struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
			SubType      string `json:"subscriptionType"`
		} `json:"oauthTokens"`
	}
	if err := json.Unmarshal(data, &creds); err != nil || len(creds.OAuthTokens) == 0 {
		return nil
	}
	t := creds.OAuthTokens[0]
	return &types.Credentials{
		Type: types.CredTypeOAuth, AccessToken: t.AccessToken,
		RefreshToken: t.RefreshToken, ExpiresAt: t.ExpiresAt, SubscriptionType: t.SubType,
	}
}

// ── Utilities ────────────────────────────────────────────────────────────

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
