package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	llm "github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// Ensure Anthropic implements Provider at compile time.
var _ Provider = (*Anthropic)(nil)

// Anthropic implements Provider for the Anthropic Messages API.
type Anthropic struct {
	client *http.Client
	apiKey string
	cache  map[string]types.ModelMeta
	mu     sync.RWMutex
}

const anthropicAPIKeyEnv = "ANTHROPIC_API_KEY"

func NewAnthropic() *Anthropic {
	a := &Anthropic{client: llm.NewProviderClient(0), cache: make(map[string]types.ModelMeta)}
	// ResolveCredentials populates a.apiKey from env or file
	a.ResolveCredentials() //nolint:errcheck
	return a
}

func (a *Anthropic) Name() string        { return "anthropic" }
func (a *Anthropic) DisplayName() string { return "Anthropic" }
func (a *Anthropic) Description() string { return describeState(a) }
func (a *Anthropic) ActivationSource() ActivationSource {
	_, src := resolveAPIKey("anthropic", anthropicAPIKeyEnv)
	return src
}
func (a *Anthropic) IsActive() bool {
	_, err := a.ResolveCredentials()
	return err == nil
}

func (a *Anthropic) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

func (a *Anthropic) ResolveCredentials() (types.Credentials, error) {
	if a.apiKey != "" {
		return types.APIKeyCredentials(a.apiKey), nil
	}
	if v, src := resolveAPIKey("anthropic", anthropicAPIKeyEnv); src != ActivationNone {
		a.apiKey = v
		return types.APIKeyCredentials(v), nil
	}
	return types.Credentials{}, fmt.Errorf("no credentials found")
}

func (a *Anthropic) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("anthropic expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}

	// Validate first (in-memory only, no disk write)
	a.apiKey = creds.APIKey
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta)
	a.mu.Unlock()
	if _, err := a.FetchModels(); err != nil {
		a.apiKey = "" // clear in-memory
		return fmt.Errorf("invalid credentials: %w", err)
	}

	// Persist to disk only after validation succeeded
	return storeAPIKey("anthropic", creds.APIKey)
}

func (a *Anthropic) Disconnect() error {
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta)
	a.mu.Unlock()
	a.apiKey = ""
	return deleteCredential("anthropic")
}

func (a *Anthropic) Models() []types.ModelMeta {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(a.cache))
	for _, m := range a.cache {
		out = append(out, m)
	}
	return out
}

func (a *Anthropic) ModelMeta(modelID string) *types.ModelMeta {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if m, ok := a.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

func (a *Anthropic) FetchModels() ([]types.ModelMeta, error) {
	metas, err := fetchAnthropicModels(a.apiKey)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		a.cache[m.ID] = m
	}
	a.mu.Unlock()
	return metas, nil
}

func (a *Anthropic) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	meta := a.ModelMeta(req.Model)
	thinkingFull := llm.BuildAnthropicThinkingFromMeta(meta, req.ThinkingLevel, req.MaxTokens)

	extraHeaders := map[string]string{}
	// Interleaved-thinking beta only for non-adaptive models
	if req.ThinkingLevel != "" && req.ThinkingLevel != "off" && thinkingFull.OutputConfig == nil {
		extraHeaders["anthropic-beta"] = "interleaved-thinking-2025-05-14"
	}

	anthrReq := &llm.AnthropicRequest{
		Request:        req,
		ThinkingConfig: &thinkingFull,
	}
	return llm.DoAnthropicStream(ctx, a.client, anthropicAPI, a.apiKey, anthrReq, extraHeaders, cb)
}

const anthropicAPI = "https://api.anthropic.com/v1/messages"

// anthropicAllow is the proxy allowlist of the Anthropic Messages API, shared
// by the API-key and OAuth providers.
var anthropicAllow = allowList("POST /v1/messages", "POST /v1/messages/count_tokens", "GET /v1/models", "GET /v1/models/:id")

// Dialect implements Provider.
func (a *Anthropic) Dialect() string { return DialectAnthropic }

// Upstream implements Provider: Anthropic's native API, authenticated with the
// stored API key. anthropic-version is a default, so an SDK that pins its own
// keeps it.
func (a *Anthropic) Upstream() (Upstream, error) {
	creds, err := a.ResolveCredentials()
	if err != nil || creds.APIKey == "" {
		return Upstream{}, fmt.Errorf("%w: anthropic has no API key", ErrNoUpstream)
	}
	return Upstream{
		BaseURL:  "https://api.anthropic.com",
		Headers:  map[string]string{"x-api-key": creds.APIKey},
		Defaults: map[string]string{"anthropic-version": "2023-06-01"},
		Allowed:  anthropicAllow,
		Client:   a.client,
	}, nil
}

// anthropicEffortLevelKeys are the effort level names fetchAnthropicModels
// looks for under capabilities.effort in GET /v1/models' response — kept
// as a package-level var (not inline in applyAnthropicCapabilities) so it
// mirrors internal/providers/llm's anthropicEffortOrder as the single
// source of truth for "which levels harness knows about", without an
// import cycle between the two packages.
var anthropicEffortLevelKeys = []string{"low", "medium", "high", "xhigh", "max"}

// applyAnthropicCapabilities fills meta's capability fields from the raw
// capabilities object GET /v1/models returns for one model — pulled out of
// fetchAnthropicModels as a pure function (no network) so the parsing
// logic is unit-testable against a captured real response shape without a
// live API call. caps may be nil (model had no capabilities object at
// all); every lookup degrades gracefully to the zero value in that case.
func applyAnthropicCapabilities(meta *types.ModelMeta, caps map[string]any) {
	if caps == nil {
		return
	}
	// Vision
	if img, ok := caps["image_input"].(map[string]any); ok {
		meta.Vision, _ = img["supported"].(bool)
	}
	// Thinking capabilities
	if t, ok := caps["thinking"].(map[string]any); ok {
		meta.Thinking, _ = t["supported"].(bool)
		if types2, ok := t["types"].(map[string]any); ok {
			if adaptive, ok := types2["adaptive"].(map[string]any); ok {
				meta.ThinkingAdaptive, _ = adaptive["supported"].(bool)
			}
			if enabled, ok := types2["enabled"].(map[string]any); ok {
				meta.ThinkingLegacy, _ = enabled["supported"].(bool)
			}
		}
	}
	// Effort levels — confirmed live (GET /v1/models) shape:
	// capabilities.effort.<level>.supported, per level, for every model
	// (legacy-only models like haiku-4-5 report every level false,
	// including the overall capabilities.effort.supported). See
	// types.ModelMeta.EffortLevels' doc comment for the "absent == unknown,
	// not unsupported" contract this populates.
	if e, ok := caps["effort"].(map[string]any); ok {
		levels := map[string]bool{}
		for _, lvl := range anthropicEffortLevelKeys {
			if l, ok := e[lvl].(map[string]any); ok {
				levels[lvl], _ = l["supported"].(bool)
			}
		}
		if len(levels) > 0 {
			meta.EffortLevels = levels
		}
	}
}

func fetchAnthropicModels(tokenOrKey string) ([]types.ModelMeta, error) {
	return fetchAnthropicModelsFrom(anthropicBaseURL, "anthropic", isClaudeModelID, func(req *http.Request, attempt int) {
		req.Header.Set("anthropic-version", "2023-06-01")
		if attempt == 0 {
			req.Header.Set("x-api-key", tokenOrKey)
			return
		}
		// Second attempt: the credential may be an OAuth token, not an API key.
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+tokenOrKey)
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		req.Header.Set("x-app", "cli")
	}, 2)
}

// isClaudeModelID is the filter for Anthropic's OWN catalog: its API lists
// only Claude models, and every id starts with "c"; anything else is not a chat
// model this provider should offer. A custom provider fronting some other
// Messages-compatible endpoint cannot assume that, and keeps every model.
func isClaudeModelID(id string) bool { return len(id) > 0 && id[0] == 'c' }

// anthropicBaseURL is Anthropic's own API root.
const anthropicBaseURL = "https://api.anthropic.com"

// anthropicModelsPageSize is the page size asked of GET /v1/models (the API's
// maximum). Without it the API returns its default page of 20 and a longer
// catalog would be silently truncated.
const anthropicModelsPageSize = 1000

// anthropicModelsMaxPages bounds the pagination loop: a server that keeps
// answering has_more:true must not hold FetchModels forever.
const anthropicModelsMaxPages = 20

// fetchAnthropicModelsFrom lists models from an Anthropic Messages-dialect API
// rooted at baseURL — Anthropic itself, or any compatible endpoint, including
// another harness's provider proxy — and returns them with the capabilities,
// context and output limits the API reports. It follows the API's pagination
// (has_more / last_id → after_id) so a catalog longer than one page is complete.
//
// keep decides which model ids are offered (nil = all); auth stamps the
// credential headers on each request; attempt is the 0-based
// credential strategy tried (the API-key provider retries once as an OAuth
// bearer token, hence maxAttempts; a custom provider has a single, fixed set of
// headers and passes 1). providerName labels errors.
func fetchAnthropicModelsFrom(baseURL, providerName string, keep func(id string) bool, auth func(req *http.Request, attempt int), maxAttempts int) ([]types.ModelMeta, error) {
	var metas []types.ModelMeta
	afterID := ""
	for page := 0; page < anthropicModelsMaxPages; page++ {
		u := strings.TrimRight(baseURL, "/") + "/v1/models?limit=" + strconv.Itoa(anthropicModelsPageSize)
		if afterID != "" {
			u += "&after_id=" + url.QueryEscape(afterID)
		}
		pageMetas, hasMore, lastID, err := fetchAnthropicModelsPage(u, providerName, keep, auth, maxAttempts)
		if err != nil {
			return nil, err
		}
		metas = append(metas, pageMetas...)
		if !hasMore || lastID == "" || lastID == afterID {
			break
		}
		afterID = lastID
	}
	return metas, nil
}

// fetchAnthropicModelsAt fetches the single page served at an explicit models
// URL (a custom provider's models_url override), used exactly as given.
func fetchAnthropicModelsAt(modelsURL, providerName string, keep func(id string) bool, auth func(req *http.Request, attempt int)) ([]types.ModelMeta, error) {
	metas, _, _, err := fetchAnthropicModelsPage(modelsURL, providerName, keep, auth, 1)
	return metas, err
}

// fetchAnthropicModelsPage fetches ONE page of a models listing from the full URL u.
func fetchAnthropicModelsPage(u, providerName string, keep func(id string) bool, auth func(req *http.Request, attempt int), maxAttempts int) (metas []types.ModelMeta, hasMore bool, lastID string, err error) {
	var resp *http.Response
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, rerr := http.NewRequest("GET", u, nil)
		if rerr != nil {
			return nil, false, "", fmt.Errorf("provider unreachable")
		}
		auth(req, attempt)
		resp, err = modelListClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if attempt == maxAttempts-1 && err != nil {
			return nil, false, "", fmt.Errorf("provider unreachable")
		}
	}
	if resp == nil || resp.StatusCode == 401 || resp.StatusCode == 403 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, false, "", fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, false, "", types.NewProviderAPIError(providerName, resp.StatusCode, nil)
	}
	defer resp.Body.Close()

	// Use a flexible structure to capture the nested capabilities
	var result struct {
		Data []struct {
			ID              string         `json:"id"`
			DisplayName     string         `json:"display_name"`
			MaxInputTokens  int            `json:"max_input_tokens"`
			MaxOutputTokens int            `json:"max_tokens"`
			Capabilities    map[string]any `json:"capabilities"`
		} `json:"data"`
		HasMore bool   `json:"has_more"`
		LastID  string `json:"last_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, false, "", fmt.Errorf("failed to parse models response")
	}

	for _, m := range result.Data {
		if m.ID == "" || (keep != nil && !keep(m.ID)) {
			continue
		}
		cw, cwSrc := m.MaxInputTokens, types.SourceProvider
		if cw <= 0 {
			cw, cwSrc = 200000, types.SourceDefault
		}
		mt, mtSrc := m.MaxOutputTokens, types.SourceProvider
		if mt <= 0 {
			mt, mtSrc = 64000, types.SourceDefault
		}

		meta := types.ModelMeta{
			ID: m.ID, DisplayName: m.DisplayName,
			ContextWindow: cw, MaxTokens: mt,
		}
		meta.Sources = map[string]string{
			types.FieldContextWindow: cwSrc,
			types.FieldMaxTokens:     mtSrc,
			types.FieldDisplayName:   types.SourceProvider,
		}
		if m.DisplayName == "" {
			meta.DisplayName, meta.Sources[types.FieldDisplayName] = m.ID, types.SourceDefault
		}

		applyAnthropicCapabilities(&meta, m.Capabilities)
		// Capabilities come from the API's own capabilities object when it
		// carries one; with none, vision/thinking are simply not reported.
		capSrc := types.SourceProvider
		if m.Capabilities == nil {
			capSrc = types.SourceDefault
		}
		meta.Sources[types.FieldVision] = capSrc
		meta.Sources[types.FieldThinking] = capSrc

		llm.ApplyRegistryPricing(&meta)
		metas = append(metas, meta)
	}
	return metas, result.HasMore, result.LastID, nil
}
