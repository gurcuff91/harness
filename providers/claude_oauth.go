package providers

import (
	"github.com/gurcuff91/harness/types"
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

	"github.com/gurcuff91/harness/config"
	llm "github.com/gurcuff91/harness/providers/llm"
	
)

const (
	anthropicOAuthAPI = "https://api.anthropic.com/v1/messages"
)

// ClaudeOAuth implements llm.Provider using Claude Code's OAuth subscription.
// Makes requests indistinguishable from Claude Code — uses your existing
// subscription instead of paying API rates.
type ClaudeOAuth struct {
	client       *http.Client
	tokens       *TokenManager
	session      string // persistent session ID per harness instance
	cache        map[string]types.ModelMeta
	mu           sync.Mutex
}

func NewClaudeOAuth() (*ClaudeOAuth, error) {
	tm, err := NewTokenManager()
	if err != nil {
		return nil, fmt.Errorf("oauth init: %w", err)
	}
	c := &ClaudeOAuth{
		cache: make(map[string]types.ModelMeta),
		client:  &http.Client{Timeout: 5 * time.Minute},
		tokens:  tm,
		session: generateUUID(),
	}
	if c.IsActive() {
		c.FetchModels()
	}
	return c, nil
}

func (c *ClaudeOAuth) Name() string   { return "claude-oauth" }
func (c *ClaudeOAuth) IsActive() bool {
	return c.tokens != nil && c.tokens.creds != nil && c.tokens.creds.AccessToken != ""
}

func (c *ClaudeOAuth) CredentialType() types.CredentialType { return types.CredTypeOAuth }

func (c *ClaudeOAuth) SetCredentials(creds types.Credentials) error {
	if creds.Type != types.CredTypeOAuth {
		return fmt.Errorf("claude-oauth expects oauth credentials, got %s", creds.Type)
	}
	if creds.AccessToken == "" {
		return fmt.Errorf("access_token cannot be empty")
	}
	c.tokens.creds = &creds
	c.tokens.SaveLogin(&creds)
	c.FetchModels()
	return nil
}

func (c *ClaudeOAuth) ClearCredentials() error {
	c.tokens.creds = nil
	c.mu.Lock()
	c.cache = make(map[string]types.ModelMeta)
	c.mu.Unlock()
	return config.DeletePrefix("claude-oauth.")
}

func (c *ClaudeOAuth) ModelMeta(modelID string) *types.ModelMeta {
	if m, ok := c.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (c *ClaudeOAuth) Models() []types.ModelMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]types.ModelMeta, 0, len(c.cache))
	for _, m := range c.cache {
		out = append(out, m)
	}
	return out
}

// Complete sends a non-streaming request (fallback).
func (c *ClaudeOAuth) Complete(ctx context.Context, req *types.Request) (*types.Response, error) {
	return c.CompleteStream(ctx, req, nil)
}

// CompleteStream sends a streaming request and emits events via callback.
// If cb is nil, it behaves like Complete (collects everything silently).
func (c *ClaudeOAuth) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	c.mu.Lock()

	// Ensure token is fresh
	token, err := c.tokens.GetValidToken()
	if err != nil {
		return nil, fmt.Errorf("oauth token: %w", err)
	}

	// Convert tools with MCP stealth naming
	var aTools []llm.AnthropicTool
	for _, t := range req.Tools {
		aTools = append(aTools, llm.AnthropicTool{
			Name:        mapToolNameToCC(t.Name),
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	// Build system blocks (stable for prompt caching)
	systemBlocks := []map[string]any{
		{
			"type":          "text",
			"text":          "You are Claude Code, Anthropic's official CLI for Claude.",
			"cache_control": map[string]string{"type": "ephemeral"},
		},
	}
	if req.SystemPrompt != "" {
		systemBlocks = append(systemBlocks, map[string]any{
			"type":          "text",
			"text":          req.SystemPrompt,
			"cache_control": map[string]string{"type": "ephemeral"},
		})
	}

	// Build thinking config based on model generation
	thinkingCfg, maxTokens := llm.BuildAnthropicThinking(req.Model, req.ThinkingLevel, req.MaxTokens)

	wireMsgs := make([]json.RawMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		for _, w := range llm.TranslateMessageToAnthropic(m) {
			wireMsgs = append(wireMsgs, w)
		}
	}

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

	// Claude Code identity headers
	billingHeader := buildBillingHeader(wireMsgs)
	setCCHeaders(httpReq, token, billingHeader, c.session)

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic OAuth API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Parse SSE stream
	return c.parseStream(httpResp.Body, cb)
}

// parseStream processes Anthropic SSE events into a types.Response.
func (c *ClaudeOAuth) parseStream(body io.Reader, cb types.StreamCallback) (*types.Response, error) {
	return llm.ParseAnthropicStream(body, cb, unmapToolNameFromCC)
}

func (c *ClaudeOAuth) FormatUserMessage(text string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"role": "user", "content": text})
	return data
}

func (c *ClaudeOAuth) FormatUserMessageWithImages(text string, images []types.ImageData) json.RawMessage {
	var content []map[string]any
	for _, img := range images {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]string{
				"type": "base64", "media_type": img.MimeType, "data": img.Base64,
			},
		})
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	data, _ := json.Marshal(map[string]any{"role": "user", "content": content})
	return data
}

// ============================================================
// Claude Code Identity & Stealth
// ============================================================

const (
	billingSalt   = "59cf53e54c78"
	mcpPrefix     = "mcp__extensions__"
)

// CC-native tools that keep their canonical casing
var ccToolNames = map[string]string{
	"read": "Read", "write": "Write", "edit": "Edit", "bash": "Bash",
	"grep": "Grep", "glob": "Glob", "askuserquestion": "AskUserQuestion",
	"enterplanmode": "EnterPlanMode", "exitplanmode": "ExitPlanMode",
	"killshell": "KillShell", "notebookedit": "NotebookEdit",
	"skill": "Skill", "task": "Task", "taskoutput": "TaskOutput",
	"todowrite": "TodoWrite", "webfetch": "WebFetch", "websearch": "WebSearch",
}

var ccVersion = envOrDefault("ANTHROPIC_CLI_VERSION", "2.1.90")

// mapToolNameToCC converts harness tool names to Claude Code format.
// CC-native tools keep canonical casing; everything else gets MCP prefix.
func mapToolNameToCC(name string) string {
	if cc, ok := ccToolNames[strings.ToLower(name)]; ok {
		return cc
	}
	return mcpPrefix + name
}

// unmapToolNameFromCC reverses the tool name mapping.
func unmapToolNameFromCC(name string) string {
	if strings.HasPrefix(name, mcpPrefix) {
		return strings.TrimPrefix(name, mcpPrefix)
	}
	// Reverse CC canonical names
	for original, cc := range ccToolNames {
		if name == cc {
			return original
		}
	}
	return name
}

// setCCHeaders sets all headers to make the request look like Claude Code.
func setCCHeaders(req *http.Request, token, billingHeader, sessionID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	req.Header.Set("x-app", "cli")
	req.Header.Set("user-agent", "claude-cli/"+ccVersion+" (external, cli)")
	req.Header.Set("x-client-request-id", generateUUID())
	req.Header.Set("X-Claude-Code-Session-Id", sessionID)
	if billingHeader != "" {
		req.Header.Set("x-anthropic-billing-header", billingHeader)
	}
}

// buildBillingHeader creates the billing header that Anthropic uses to
// verify this is a Claude Code session (uses subscription, not extra usage).
func buildBillingHeader(messages []json.RawMessage) string {
	text := extractFirstUserText(messages)
	sampled := sampleChars(text, []int{4, 7, 20})
	suffixInput := billingSalt + sampled + ccVersion
	suffix := sha256hex(suffixInput)[:3]
	cch := sha256hex(text)[:5]
	return fmt.Sprintf("cc_version=%s.%s; cc_entrypoint=cli; cch=%s;", ccVersion, suffix, cch)
}

// extractFirstUserText gets the text from the first user message.
func extractFirstUserText(messages []json.RawMessage) string {
	for _, raw := range messages {
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Role != "user" {
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
	var result []rune
	for _, i := range indices {
		if i < len(runes) {
			result = append(result, runes[i])
		} else {
			result = append(result, '0')
		}
	}
	return string(result)
}

func sha256hex(input string) string {
	h := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", h)
}

// ============================================================
// Utilities
// ============================================================

func generateUUID() string {
	// Simple UUID v4 using crypto/rand via os
	b := make([]byte, 16)
	f, _ := os.Open("/dev/urandom")
	defer f.Close()
	f.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ============================================================
// Keychain reader (macOS) — exported for oauth package

const (
	oauthTokenURL  = "https://claude.ai/v1/oauth/token"
	oauthClientID  = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	expiryBufferMs = 60_000
)

// TokenManager handles OAuth token lifecycle using credentials.json.
type TokenManager struct {
	mu    sync.Mutex
	creds *types.Credentials
}

func NewTokenManager() (*TokenManager, error) {
	tm := &TokenManager{}
	at, _ := config.LoadCred("claude-oauth.access_token")
	rt, _ := config.LoadCred("claude-oauth.refresh_token")
	var ea int64
	if eas, ok := config.LoadCred("claude-oauth.expires_at"); ok {
		fmt.Sscanf(eas, "%d", &ea)
	}
	st, _ := config.LoadCred("claude-oauth.subscription_type")
	var c *types.Credentials
	if at != "" {
		c = &types.Credentials{Type: types.CredTypeOAuth, AccessToken: at, RefreshToken: rt, ExpiresAt: ea, SubscriptionType: st}
	}
	if c != nil {
		tm.creds = c
	}
	return tm, nil
}

func (tm *TokenManager) GetValidToken() (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.creds == nil {
		return "", fmt.Errorf("claude-oauth not connected — use /connect claude-oauth")
	}
	if tm.creds.ExpiresAt > time.Now().UnixMilli()+expiryBufferMs {
		return tm.creds.AccessToken, nil
	}
	refreshed, err := tm.refreshToken(tm.creds.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("token expired — use /connect claude-oauth")
	}
	tm.creds = refreshed
	tm.SaveLogin(refreshed)
	return refreshed.AccessToken, nil
}

func (tm *TokenManager) GetTokenInfo() (expiresAt int64, subType string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.creds == nil {
		return 0, ""
	}
	return tm.creds.ExpiresAt, tm.creds.SubscriptionType
}

func (tm *TokenManager) refreshToken(refreshToken string) (*types.Credentials, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {oauthClientID},
		"refresh_token": {refreshToken},
	}
	resp, err := http.Post(oauthTokenURL, "application/x-www-form-urlencoded", bytes.NewBufferString(form.Encode()))
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
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("no access_token in refresh response")
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
		AccessToken:      result.AccessToken,
		RefreshToken:     newRefresh,
		ExpiresAt:        time.Now().UnixMilli() + int64(expiresIn)*1000,
		SubscriptionType: tm.creds.SubscriptionType,
	}, nil
}

func (tm *TokenManager) SaveLogin(creds *types.Credentials) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.creds = creds
	config.StoreCred("claude-oauth.access_token", creds.AccessToken)
	config.StoreCred("claude-oauth.refresh_token", creds.RefreshToken)
	config.StoreCred("claude-oauth.expires_at", fmt.Sprintf("%d", creds.ExpiresAt))
	config.StoreCred("claude-oauth.subscription_type", creds.SubscriptionType)
	return nil
}

// ── OAuth Login ──────────────────────────────────────────

// RunOAuthFlow executes the OAuth flow and returns the resulting credentials.
// Called by the CLI transport — not interactive inside the provider itself.
func RunOAuthFlow() *types.Credentials {
	if err := Login(); err != nil {
		return nil
	}
	tm, _ := NewTokenManager()
	if tm == nil || tm.creds == nil {
		return nil
	}
	return tm.creds
}

// Login authenticates via Claude Code's official login flow,
// then imports the resulting tokens into ~/.harness/credentials.json
func Login() error {
	fmt.Println("\n  🔑 Starting authentication via Claude Code...")

	cmd := exec.Command("claude", "auth", "login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()

	// Reset terminal — claude may have changed settings
	resetTerminal()

	if err != nil {
		return fmt.Errorf("claude auth login failed: %w\n  Install: npm install -g @anthropic-ai/claude-code", err)
	}

	tm, _ := NewTokenManager()
	token := readClaudeFromKeychain()
	if token == nil {
		token = readClaudeCredentialsFile()
	}
	if token == nil {
		return fmt.Errorf("login completed but couldn't import tokens — try again")
	}

	tm.SaveLogin(token)
	RefreshProviderModels("claude-oauth")

	// Auto-select first model if user hasn't chosen one yet
	s := config.ReadSettings()
	if s.Model == "" {
		RefreshProviderModels("claude-oauth")
		for _, p := range All {
			if p.Name() == "claude-oauth" && len(p.Models()) > 0 {
				fullName := "claude-oauth/" + p.Models()[0].ID
				config.SetActiveModel(fullName)
				fmt.Printf("  Auto-selected: %s\n", fullName)
				break
			}
		}
	}

	fmt.Println("\n  ✅ Authenticated! Tokens saved.")
	return nil
}

func resetTerminal() {
	exec.Command("stty", "sane").Run()
	fmt.Print("\033[?25h")
	fmt.Print("\033[0m")
}

func readClaudeFromKeychain() *types.Credentials {
	// Primary: Claude Code credentials
	if t := readKeychainItem("Claude Code-credentials"); t != nil {
		return t
	}
	// Fallback: older keychain name
	return readKeychainItem("claude-code")
}

func readKeychainItem(service string) *types.Credentials {
	out, err := exec.Command("security", "find-generic-password",
		"-s", service, "-w").Output()
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
	return &types.Credentials{Type: types.CredTypeOAuth, AccessToken: at, RefreshToken: rt, ExpiresAt: int64(ea), SubscriptionType: st}
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
	return &types.Credentials{Type: types.CredTypeOAuth, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: t.ExpiresAt, SubscriptionType: t.SubType}
}


// FetchModels returns all models available via Claude OAuth.
// Capabilities are authoritative from the API; pricing comes from llm-registry.
func (c *ClaudeOAuth) FetchModels() []types.ModelMeta {
	c.mu.Lock()
	defer c.mu.Unlock()

	tm, err := NewTokenManager()
	if err != nil || tm == nil {
		return nil
	}
	tok, err := tm.GetValidToken()
	if err != nil {
		return nil
	}
	metas := fetchAnthropicModels(tok)
	c.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		c.cache[m.ID] = m
	}
	return metas
}
