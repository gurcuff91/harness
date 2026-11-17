package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	llm "github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// Synthetic (synthetic.new) serves open-weight models behind an OpenAI Chat Completions API.
//
// Everything here was MEASURED against the live API, not read from its documentation, which leaves most of
// the contract out (see the 2026-11-17 entry of the CHANGELOG for the full list). What matters to this file:
//
//   - The model catalog (GET /models) is PUBLIC: it answers 200 with no key and with a bad one, so listing
//     models proves nothing about a key. A key is validated by a real, cheap chat request instead.
//   - Errors are {"error":"<plain string>"}, not OpenAI's {"error":{"message":...}}.
//   - A 402 (no credits and no plan) and a 429 (too many concurrent requests, no Retry-After) both mean the
//     key is GOOD. Only a 401 means it is not.
//   - Pricing comes as a string per TOKEN ("$0.0000006"); ModelMeta stores USD per MILLION tokens.
//   - Reasoning comes back in delta.reasoning (the shared parser reads both that and reasoning_content), and
//     the only request field that controls it is reasoning_effort: think/thinking are ignored.
//   - usage is on every chunk, cumulative, and the last chunk has choices: [] (the shared parser keeps the
//     last one). It carries prompt_tokens_details.cached_tokens, which the shared parser now reads.
const (
	syntheticURL       = "https://api.synthetic.new/openai/v1"
	syntheticQuotasURL = "https://api.synthetic.new/v2/quotas"

	syntheticAPIKeyEnv = "SYNTHETIC_API_KEY"

	// syntheticProbeModel is the model a key is validated with: the cheapest one, one output token.
	syntheticProbeModel = "hf:zai-org/GLM-4.7-Flash"
)

// Synthetic implements Provider for synthetic.new's OpenAI-compatible API.
type Synthetic struct {
	apiKey string
	// client streams chat completions (no overall timeout: a turn may run for minutes); listClient makes the
	// short, non-streaming calls (key validation, the catalog, the quotas) and has a total timeout so a
	// stalled one can never hang the model listing.
	client     *http.Client
	listClient *http.Client
	// baseURL and quotasURL are the two endpoints, fields only so a test can point them at a fake.
	baseURL   string
	quotasURL string
	cache     map[string]types.ModelMeta
	mu        sync.RWMutex
}

func NewSynthetic() *Synthetic {
	return &Synthetic{
		client:     llm.NewProviderClient(0),
		listClient: llm.NewProviderClient(15 * time.Second),
		baseURL:    syntheticURL,
		quotasURL:  syntheticQuotasURL,
		cache:      make(map[string]types.ModelMeta),
	}
}

func (o *Synthetic) Name() string        { return "synthetic" }
func (o *Synthetic) DisplayName() string { return "Synthetic" }
func (o *Synthetic) Description() string { return describeState(o) }
func (o *Synthetic) ActivationSource() ActivationSource {
	_, src := resolveAPIKey("synthetic", syntheticAPIKeyEnv)
	return src
}
func (o *Synthetic) IsActive() bool {
	_, err := o.ResolveCredentials()
	return err == nil
}

func (o *Synthetic) CredentialType() types.CredentialType { return types.CredTypeAPIKey }

// Dialect implements Provider.
func (o *Synthetic) Dialect() string { return DialectOpenAI }

// Upstream implements Provider: Synthetic's OpenAI-compatible API with the stored key. It needs no other
// header (measured: no User-Agent, Referer or client-identification header is required).
func (o *Synthetic) Upstream() (Upstream, error) {
	creds, err := o.ResolveCredentials()
	if err != nil || creds.APIKey == "" {
		return Upstream{}, fmt.Errorf("%w: synthetic has no API key", ErrNoUpstream)
	}
	return Upstream{
		BaseURL: o.baseURL,
		Headers: map[string]string{"Authorization": "Bearer " + creds.APIKey},
		Allowed: openAIStyleAllow,
		Client:  o.client,
	}, nil
}

func (o *Synthetic) ResolveCredentials() (types.Credentials, error) {
	if o.apiKey != "" {
		return types.APIKeyCredentials(o.apiKey), nil
	}
	if v, src := resolveAPIKey("synthetic", syntheticAPIKeyEnv); src != ActivationNone {
		o.apiKey = v
		return types.APIKeyCredentials(v), nil
	}
	return types.Credentials{}, fmt.Errorf("no credentials found")
}

func (o *Synthetic) Connect(creds types.Credentials) error {
	if creds.Type != types.CredTypeAPIKey {
		return fmt.Errorf("synthetic expects api_key credentials, got %s", creds.Type)
	}
	if creds.APIKey == "" {
		return fmt.Errorf("api_key cannot be empty")
	}
	o.apiKey = creds.APIKey
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	if _, err := o.FetchModels(); err != nil {
		o.apiKey = ""
		return fmt.Errorf("invalid credentials: %w", err)
	}
	return storeAPIKey("synthetic", creds.APIKey)
}

func (o *Synthetic) Disconnect() error {
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta)
	o.mu.Unlock()
	o.apiKey = ""
	return deleteCredential("synthetic")
}

func (o *Synthetic) Models() []types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]types.ModelMeta, 0, len(o.cache))
	for _, m := range o.cache {
		out = append(out, m)
	}
	return out
}

func (o *Synthetic) ModelMeta(modelID string) *types.ModelMeta {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.cache[modelID]; ok {
		cp := m
		return &cp
	}
	return nil
}

// FetchModels validates the key, lists the models and decides whether the account is a subscription.
func (o *Synthetic) FetchModels() ([]types.ModelMeta, error) {
	// /models is public, so the key is checked with a real request first (see the file comment).
	if err := o.validateKey(); err != nil {
		return nil, err
	}
	metas, err := fetchSyntheticModels(o.baseURL+"/models", o.listClient)
	if err != nil {
		return nil, err
	}
	// A subscription account has quotas and pays a flat fee; a credits-only account pays per token. The quotas
	// endpoint is the only place that tells them apart, and it is undocumented for everything but the
	// subscription case, so ANY failure or unexpected shape means "not known to be a subscription": it must
	// never break connecting a key that works.
	if syntheticIsSubscription(o.apiKey, o.quotasURL, o.listClient) {
		markAllSubscription(metas)
	}
	o.mu.Lock()
	o.cache = make(map[string]types.ModelMeta, len(metas))
	for _, m := range metas {
		o.cache[m.ID] = m
	}
	o.mu.Unlock()
	return metas, nil
}

// validateKey proves the key with one chat request of one output token on the cheapest model.
//
// 401 is the only answer that means "this key is not valid". A 402 (no credits and no plan) and a 429
// (concurrency limit) both come from an account that EXISTS: the key is good and connecting must succeed,
// leaving the user to add credit, or to retry, rather than telling them their key is wrong. Anything else
// that is not a 200 is the provider failing, and is reported as such.
func (o *Synthetic) validateKey() error {
	body := strings.NewReader(`{"model":"` + syntheticProbeModel + `","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"reasoning_effort":"none"}`)
	req, err := http.NewRequest("POST", o.baseURL+"/chat/completions", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.listClient.Do(req)
	if err != nil {
		return fmt.Errorf("provider unreachable")
	}
	defer resp.Body.Close()
	return syntheticKeyVerdict(resp.StatusCode, resp.Body)
}

// syntheticKeyVerdict maps the status of the validation request to "is this key usable".
func syntheticKeyVerdict(status int, body io.Reader) error {
	switch {
	case status == http.StatusOK, status == http.StatusPaymentRequired, status == http.StatusTooManyRequests:
		return nil
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return fmt.Errorf("invalid API key")
	}
	b, _ := io.ReadAll(io.LimitReader(body, 4096))
	return syntheticAPIError(status, b)
}

// CompleteStream sends the request through the shared OpenAI-dialect stream, with the thinking level
// translated by syntheticRequest (see there).
func (o *Synthetic) CompleteStream(ctx context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	return llm.DoOpenAIStream(ctx, o.client, o.baseURL+"/chat/completions", o.apiKey,
		&llm.OpenAIRequest{Request: req, VerbatimReasoningEffort: true, ProviderName: "synthetic", ErrorText: syntheticErrorText}, nil, cb)
}

// Thinking. MEASURED on all seven models: the ONLY field that controls reasoning is reasoning_effort.
// think:false and thinking:{type:disabled} change nothing, and the shared body builder's DeepSeek rule
// (every level but "max" becomes "high", a rule of DeepSeek's own API) is wrong here: Synthetic's DeepSeek
// lists none, low, high, xhigh and max and accepts low and xhigh as themselves. So the request opts into
// VerbatimReasoningEffort: the thinking level goes out as reasoning_effort as it is, "off" as "none", and
// nothing else is added.
//
// "none" switches reasoning off completely on GLM-4.7-Flash, DeepSeek and Qwen only. Kimi, gpt-oss and
// GLM-5.3 accept it (HTTP 200) and keep reasoning a little: those models cannot be turned fully off, and a
// level a model does not list is accepted too, so nothing is clamped to the catalog's list.

// ── model catalog ──────────────────────────────────────────────────────────────────────────────────────

// syntheticCatalogModel is the part of a /models entry harness reads.
type syntheticCatalogModel struct {
	ID               string          `json:"id"`
	DisplayName      string          `json:"display_name"`
	ContextLength    int             `json:"context_length"`
	MaxOutputLength  int             `json:"max_output_length"`
	InputModalities  []string        `json:"input_modalities"`
	OutputModalities []string        `json:"output_modalities"`
	SupportedFeature []string        `json:"supported_features"`
	Pricing          json.RawMessage `json:"pricing"`
}

// syntheticPricing is the per-token price block. Every value is a string ("$0.0000006", "0").
type syntheticPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
	CacheReads string `json:"input_cache_reads"`
	CacheWrite string `json:"input_cache_writes"`
}

// fetchSyntheticModels lists the models harness offers. The catalog carries three families of id and
// only one is a model: "hf:" is the concrete model; "syn:" (aliases the server resolves to a recommended
// model, which changes over time) and "infinite:" (an alias that needs a plan and streaming) route to
// models that are already listed under their own "hf:" id, so listing them would only duplicate them.
// Anything that does not produce text (an embeddings model, an image generator) cannot hold a
// conversation and is left out too: it is decided by the model's OUTPUT, not by a word in its name.
func fetchSyntheticModels(url string, client *http.Client) ([]types.ModelMeta, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, syntheticAPIError(resp.StatusCode, b)
	}
	var result struct {
		Data []syntheticCatalogModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse models response")
	}
	return syntheticModels(result.Data), nil
}

// syntheticModels filters and converts catalog entries (pure, so it is testable without a server).
func syntheticModels(entries []syntheticCatalogModel) []types.ModelMeta {
	metas := make([]types.ModelMeta, 0, len(entries))
	for _, e := range entries {
		if !syntheticOffered(e) {
			continue
		}
		metas = append(metas, syntheticMeta(e))
	}
	return metas
}

// syntheticOffered reports whether a catalog entry is a model harness offers: an "hf:" id that produces text.
func syntheticOffered(e syntheticCatalogModel) bool {
	if !strings.HasPrefix(e.ID, "hf:") {
		return false
	}
	for _, m := range e.OutputModalities {
		if m == "text" {
			return true
		}
	}
	return false
}

// syntheticMeta converts one offered catalog entry. Every field the catalog reports is authoritative.
func syntheticMeta(e syntheticCatalogModel) types.ModelMeta {
	m := types.ModelMeta{
		ID:            e.ID,
		DisplayName:   e.DisplayName,
		ContextWindow: e.ContextLength,
		MaxTokens:     e.MaxOutputLength,
		Sources:       map[string]string{},
	}
	if m.DisplayName == "" {
		m.DisplayName = e.ID
		m.Sources[types.FieldDisplayName] = types.SourceDefault
	} else {
		m.Sources[types.FieldDisplayName] = types.SourceProvider
	}
	for _, mod := range e.InputModalities {
		if mod == "image" {
			m.Vision = true
		}
	}
	for _, f := range e.SupportedFeature {
		if f == "reasoning" {
			m.Thinking = true
		}
	}
	// The catalog reports capabilities and limits for every model, so absence here is a real "no", and a
	// missing limit is a gap to fill from the registry (the cascade only fills what is still empty).
	m.Sources[types.FieldVision] = types.SourceProvider
	m.Sources[types.FieldThinking] = types.SourceProvider
	if m.ContextWindow > 0 {
		m.Sources[types.FieldContextWindow] = types.SourceProvider
	}
	if m.MaxTokens > 0 {
		m.Sources[types.FieldMaxTokens] = types.SourceProvider
	}

	var p syntheticPricing
	if len(e.Pricing) > 0 && json.Unmarshal(e.Pricing, &p) == nil {
		in, okIn := syntheticPerMillion(p.Prompt)
		out, okOut := syntheticPerMillion(p.Completion)
		if okIn && okOut {
			m.InputPrice, m.OutputPrice = in, out
			if v, ok := syntheticPerMillion(p.CacheReads); ok {
				m.CacheRead = v
			}
			if v, ok := syntheticPerMillion(p.CacheWrite); ok {
				m.CacheWrite = v
			}
			m.Sources[types.FieldPricing] = types.SourceProvider
		}
	}
	if _, ok := m.Sources[types.FieldPricing]; !ok {
		m.Sources[types.FieldPricing] = types.SourceUnknown
	}
	// Only when Synthetic gave NO usable price does the OpenRouter catalog get a say. A price of 0 that
	// Synthetic reported is its answer ("free"), not a gap: the source is what tells the two apart.
	if m.Sources[types.FieldPricing] == types.SourceUnknown {
		llm.ApplyRegistryPricing(&m)
	}
	return m
}

// syntheticPerMillion converts a per-token price string ("$0.0000006", "0", "") to USD per million tokens.
// ok is false for anything that is not a non-negative number, so a malformed price is "unknown" and never a
// wrong number.
func syntheticPerMillion(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v != v {
		return 0, false
	}
	return roundPerMillion(v * 1e6), true
}

// roundPerMillion drops the float noise of the multiplication (0.0000006*1e6 is 0.6000000000000001).
func roundPerMillion(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 6, 64), 64)
	return r
}

// ── subscription ───────────────────────────────────────────────────────────────────────────────────────

// syntheticIsSubscription asks GET /v2/quotas whether the account has a plan. The documented answer for a
// subscription is {"subscription":{"limit":..,"requests":..,"renewsAt":..}}; this account (credits only,
// no plan) answers 200 with {}. It is true ONLY for a 200 whose body has a "subscription" key holding a
// JSON object. A network error, any other status, a body that is not JSON, or a null or non-object
// "subscription" are all "not known to be one": a pay-per-use reading is the safe default, and this call
// must never be the reason a working key fails to connect. Quota requests do not count against the
// account's limits (per Synthetic's documentation).
func syntheticIsSubscription(apiKey, url string, client *http.Client) bool {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false
	}
	return syntheticHasSubscription(b)
}

// syntheticHasSubscription reports whether a quotas body carries a subscription object.
func syntheticHasSubscription(body []byte) bool {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return false
	}
	raw, ok := q["subscription"]
	if !ok {
		return false
	}
	var sub map[string]json.RawMessage
	return json.Unmarshal(raw, &sub) == nil && sub != nil
}

// ── errors ────────────────────────────────────────────────────────────────────────────────────────────

// syntheticErrorText extracts the message of a Synthetic error body. Synthetic answers {"error":"<text>"}
// with a plain string, where the shared error builder expects an object, so without this the user would
// see the status and the raw JSON. The text is the provider's own and is not trusted (it is cleaned of
// control characters by types.NewProviderAPIError, which this feeds).
func syntheticErrorText(body []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.Error, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &obj) == nil {
		return strings.TrimSpace(obj.Message)
	}
	return ""
}

// syntheticAPIError builds the error for a non-200 answer, with the provider's own text when it has one.
func syntheticAPIError(status int, body []byte) error {
	if text := syntheticErrorText(body); text != "" {
		return types.NewProviderAPIError("synthetic", status, []byte(text))
	}
	return types.NewProviderAPIError("synthetic", status, body)
}
