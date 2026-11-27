package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// Every fixture below is what api.synthetic.new ANSWERED when it was probed live (2026-11-17), trimmed to
// the fields harness reads. Nothing here needs the network or a key.

// catalogJSON is the real /openai/v1/models shape: 7 hf: models, the 4 syn: aliases and infinite:small:vision,
// plus an embeddings entry (the docs list one that /models does not return today, so it is invented here to
// pin the filter for the day it does).
const catalogJSON = `{"data":[
 {"provider":"synthetic","display_name":"DeepSeek V4.1 Flash 10/17","id":"infinite:small:vision","input_modalities":["text","image"],"output_modalities":["text"],"context_length":524288,"max_output_length":65536,"supported_features":["tools","reasoning"],"pricing":{"prompt":"0","completion":"0","image":"0","request":"0","input_cache_reads":"0","input_cache_writes":"0"}},
 {"display_name":"DeepSeek V4.1 Flash 10/17","id":"syn:large:text","alias_of":"DeepSeek V4.1 Flash 10/17","input_modalities":["text","image"],"output_modalities":["text"],"context_length":524288,"max_output_length":65536,"supported_features":["tools","reasoning"],"pricing":{"prompt":"$0.0000006","completion":"$0.0000012","input_cache_reads":"$0.00000003","input_cache_writes":"0"}},
 {"display_name":"GLM 4.7 Flash","id":"syn:small:text","input_modalities":["text"],"output_modalities":["text"],"context_length":196608,"max_output_length":65536,"supported_features":["tools","reasoning"],"pricing":{"prompt":"$0.0000001","completion":"$0.0000005","input_cache_reads":"$0.00000002","input_cache_writes":"0"}},
 {"display_name":"GLM 4.7 Flash","id":"hf:zai-org/GLM-4.7-Flash","input_modalities":["text"],"output_modalities":["text"],"context_length":196608,"max_output_length":65536,"supported_features":["tools","json_mode","structured_outputs","reasoning"],"pricing":{"prompt":"$0.0000001","completion":"$0.0000005","image":"0","request":"0","input_cache_reads":"$0.00000002","input_cache_writes":"0"}},
 {"display_name":"Kimi K3.1 Pro","id":"hf:moonshotai/Kimi-K3.1-Pro","input_modalities":["text","image"],"output_modalities":["text"],"context_length":524288,"max_output_length":65536,"supported_features":["tools","json_mode","structured_outputs","reasoning"],"pricing":{"prompt":"$0.000002","completion":"$0.00001","input_cache_reads":"$0.0000002","input_cache_writes":"0"}},
 {"display_name":"gpt-oss-120b","id":"hf:openai/gpt-oss-120b","input_modalities":["text"],"output_modalities":["text"],"context_length":131072,"max_output_length":65536,"supported_features":["tools"],"pricing":{"prompt":"$0.0000001","completion":"$0.0000001","input_cache_reads":"$0.00000002","input_cache_writes":"0"}},
 {"display_name":"Nomic Embed Text v1.5","id":"hf:nomic-ai/nomic-embed-text-v1.5","input_modalities":["text"],"output_modalities":["embedding"],"context_length":8192,"max_output_length":0,"supported_features":[],"pricing":{"prompt":"$0.00000001","completion":"0"}},
 {"display_name":"An image model","id":"hf:acme/Painter","input_modalities":["text"],"output_modalities":["image"],"context_length":4096,"max_output_length":0,"supported_features":[],"pricing":{"prompt":"0","completion":"0"}}
]}`

func decodeCatalog(t *testing.T, raw string) []syntheticCatalogModel {
	t.Helper()
	var r struct {
		Data []syntheticCatalogModel `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	return r.Data
}

func byID(ms []types.ModelMeta) map[string]types.ModelMeta {
	out := map[string]types.ModelMeta{}
	for _, m := range ms {
		out[m.ID] = m
	}
	return out
}

// ── which models are offered ────────────────────────────────────────────────────────────────────────

// Khan's rule: only "hf:" models, and none that is not a text model. syn: and infinite: are aliases of
// models already listed under their hf: id; the embeddings and image entries cannot hold a conversation.
func TestOnlyHFModelsThatProduceTextAreOffered(t *testing.T) {
	got := byID(syntheticModels(decodeCatalog(t, catalogJSON)))
	want := []string{"hf:zai-org/GLM-4.7-Flash", "hf:moonshotai/Kimi-K3.1-Pro", "hf:openai/gpt-oss-120b"}
	if len(got) != len(want) {
		var ids []string
		for id := range got {
			ids = append(ids, id)
		}
		t.Fatalf("offered %v, want exactly %v", ids, want)
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("%s is not offered", id)
		}
	}
	for _, banned := range []string{"syn:large:text", "syn:small:text", "infinite:small:vision", "hf:nomic-ai/nomic-embed-text-v1.5", "hf:acme/Painter"} {
		if _, ok := got[banned]; ok {
			t.Errorf("%s is offered", banned)
		}
	}
}

// The embeddings exclusion is decided by what a model OUTPUTS, not by a word in its name: a chat model
// whose name contains "embed" stays, and an embeddings model with an ordinary name goes.
func TestEmbeddingsAreExcludedByOutputNotByName(t *testing.T) {
	got := byID(syntheticModels(decodeCatalog(t, `{"data":[
	 {"id":"hf:acme/embedded-systems-expert","display_name":"E","input_modalities":["text"],"output_modalities":["text"],"context_length":1000,"max_output_length":100},
	 {"id":"hf:acme/Vectorizer-9","display_name":"V","input_modalities":["text"],"output_modalities":["embedding"],"context_length":1000,"max_output_length":0},
	 {"id":"hf:acme/NoOutputs","display_name":"N","input_modalities":["text"],"output_modalities":[],"context_length":1000,"max_output_length":100},
	 {"id":"hf:acme/NullOutputs","display_name":"N","input_modalities":["text"],"context_length":1000,"max_output_length":100},
	 {"id":"hf:acme/TextAndImage","display_name":"T","input_modalities":["text"],"output_modalities":["text","image"],"context_length":1000,"max_output_length":100}
	]}`)))
	if _, ok := got["hf:acme/embedded-systems-expert"]; !ok {
		t.Error("a chat model called 'embedded-systems-expert' was dropped for its name")
	}
	if _, ok := got["hf:acme/TextAndImage"]; !ok {
		t.Error("a model that outputs text AND images was dropped")
	}
	for _, id := range []string{"hf:acme/Vectorizer-9", "hf:acme/NoOutputs", "hf:acme/NullOutputs"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s is offered although it does not produce text", id)
		}
	}
}

// The prefix test is exact: "hf:" at the start, case-sensitive, and not merely contained.
func TestTheHFPrefixIsExact(t *testing.T) {
	got := byID(syntheticModels(decodeCatalog(t, `{"data":[
	 {"id":"hf:a/ok","display_name":"ok","output_modalities":["text"],"context_length":1000,"max_output_length":10},
	 {"id":"HF:a/upper","display_name":"u","output_modalities":["text"],"context_length":1000,"max_output_length":10},
	 {"id":"fast:hf:a/fast","display_name":"f","output_modalities":["text"],"context_length":1000,"max_output_length":10},
	 {"id":"x-hf:a/contained","display_name":"c","output_modalities":["text"],"context_length":1000,"max_output_length":10},
	 {"id":"hf","display_name":"bare","output_modalities":["text"],"context_length":1000,"max_output_length":10},
	 {"id":"","display_name":"empty","output_modalities":["text"],"context_length":1000,"max_output_length":10}
	]}`)))
	if len(got) != 1 || got["hf:a/ok"].ID == "" {
		t.Errorf("offered %v, want only hf:a/ok", got)
	}
}

// ── metadata ────────────────────────────────────────────────────────────────────────────────────────

// Everything the catalog reports is authoritative, and the price (a string per TOKEN) becomes USD per
// MILLION tokens without float noise.
func TestModelMetadataComesFromTheCatalog(t *testing.T) {
	got := byID(syntheticModels(decodeCatalog(t, catalogJSON)))

	kimi := got["hf:moonshotai/Kimi-K3.1-Pro"]
	if kimi.DisplayName != "Kimi K3.1 Pro" || kimi.ContextWindow != 524288 || kimi.MaxTokens != 65536 {
		t.Errorf("kimi = %+v", kimi)
	}
	if !kimi.Vision || !kimi.Thinking {
		t.Errorf("kimi vision=%v thinking=%v, want both (image input, reasoning feature)", kimi.Vision, kimi.Thinking)
	}
	if kimi.InputPrice != 2 || kimi.OutputPrice != 10 || kimi.CacheRead != 0.2 || kimi.CacheWrite != 0 {
		t.Errorf("kimi prices (per million) = in %v out %v cacheRead %v cacheWrite %v, want 2 / 10 / 0.2 / 0", kimi.InputPrice, kimi.OutputPrice, kimi.CacheRead, kimi.CacheWrite)
	}

	glm := got["hf:zai-org/GLM-4.7-Flash"]
	if glm.Vision {
		t.Error("a text-only model is marked as vision")
	}
	if glm.InputPrice != 0.1 || glm.OutputPrice != 0.5 || glm.CacheRead != 0.02 {
		t.Errorf("glm prices = in %v out %v cacheRead %v, want 0.1 / 0.5 / 0.02", glm.InputPrice, glm.OutputPrice, glm.CacheRead)
	}

	// a model WITHOUT the reasoning feature is not marked as thinking (the catalog reports it for every model)
	if oss := got["hf:openai/gpt-oss-120b"]; oss.Thinking {
		t.Error("a model whose supported_features lacks reasoning is marked as thinking")
	}
}

// Provenance: every datum the catalog gave is marked as the provider's, which is what tells a client how
// far to trust a number.
func TestTheProvenanceOfTheCatalogData(t *testing.T) {
	m := byID(syntheticModels(decodeCatalog(t, catalogJSON)))["hf:moonshotai/Kimi-K3.1-Pro"]
	for _, f := range []string{types.FieldDisplayName, types.FieldContextWindow, types.FieldMaxTokens, types.FieldVision, types.FieldThinking, types.FieldPricing} {
		if m.Sources[f] != types.SourceProvider {
			t.Errorf("source of %s = %q, want %q", f, m.Sources[f], types.SourceProvider)
		}
	}
}

// The per-token string is parsed strictly: "$" optional, whitespace tolerated, a malformed or negative
// price is "unknown" and never a wrong number.
func TestPricesAreParsedStrictly(t *testing.T) {
	for in, want := range map[string]float64{
		"$0.0000006": 0.6, "0.0000006": 0.6, " $0.000002 ": 2, "$0.00001": 10, "0": 0, "$0": 0,
		"$0.0000014": 1.4, "$0.00000026": 0.26, "$0.000000045": 0.045,
	} {
		got, ok := syntheticPerMillion(in)
		if !ok || got != want {
			t.Errorf("syntheticPerMillion(%q) = (%v, %v), want (%v, true)", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " ", "$", "free", "$abc", "-0.1", "$-0.0000001", "NaN", "$NaN", "1e", "0.1.2"} {
		if got, ok := syntheticPerMillion(in); ok {
			t.Errorf("syntheticPerMillion(%q) = (%v, true), want not ok", in, got)
		}
	}
}

// A model whose price block is missing or malformed carries NO price and says so: pricing "unknown", never
// a 0 that reads as "free". (A price Synthetic reports as "0" IS an answer, and stays free.)
func TestAMissingPriceIsUnknownAndAZeroPriceIsFree(t *testing.T) {
	got := byID(syntheticModels(decodeCatalog(t, `{"data":[
	 {"id":"hf:a/nopricing","display_name":"n","output_modalities":["text"],"context_length":1000,"max_output_length":10},
	 {"id":"hf:a/badpricing","display_name":"b","output_modalities":["text"],"context_length":1000,"max_output_length":10,"pricing":{"prompt":"cheap","completion":"$0.1"}},
	 {"id":"hf:a/stringpricing","display_name":"s","output_modalities":["text"],"context_length":1000,"max_output_length":10,"pricing":"free"},
	 {"id":"hf:a/free","display_name":"f","output_modalities":["text"],"context_length":1000,"max_output_length":10,"pricing":{"prompt":"0","completion":"0","input_cache_reads":"0","input_cache_writes":"0"}}
	]}`)))
	for _, id := range []string{"hf:a/nopricing", "hf:a/badpricing", "hf:a/stringpricing"} {
		m := got[id]
		if m.InputPrice != 0 || m.OutputPrice != 0 {
			t.Errorf("%s has a price (%v/%v) it was never given", id, m.InputPrice, m.OutputPrice)
		}
		if src := m.Sources[types.FieldPricing]; src == types.SourceProvider {
			t.Errorf("%s: pricing is marked as the provider's", id)
		}
	}
	if free := got["hf:a/free"]; free.Sources[types.FieldPricing] != types.SourceProvider || free.InputPrice != 0 {
		t.Errorf("a price Synthetic reported as 0 must stay a provider-reported free: %+v", free)
	}
}

// A missing context window or output limit falls back to the cascade's defaults, marked as such, instead
// of leaving 0 (a model with a 0 window would be unusable).
func TestAMissingLimitFallsBackToTheCascade(t *testing.T) {
	m := byID(syntheticModels(decodeCatalog(t, `{"data":[{"id":"hf:a/nolimits","display_name":"n","output_modalities":["text"]}]}`)))["hf:a/nolimits"]
	_ = m
	// the cascade is applied by ApplyRegistryPricing only for pricing; limits stay 0 here and the SESSION
	// falls back to its own defaults (128k/32k). What matters is that the source is NOT the provider's.
	if m.Sources[types.FieldContextWindow] == types.SourceProvider || m.Sources[types.FieldMaxTokens] == types.SourceProvider {
		t.Errorf("a limit the catalog did not give is marked as the provider's: %v", m.Sources)
	}
}

// ── a recording fake of the whole API ───────────────────────────────────────────────────────────────

type synthFake struct {
	*httptest.Server
	mu        sync.Mutex
	chatBody  []byte
	chatAuth  string
	chatCalls int
	quotas    string
	quotaCode int
	chatCode  int
	chatBodyR string
}

func newSynthFake(t *testing.T) *synthFake {
	t.Helper()
	f := &synthFake{quotas: `{}`, quotaCode: 200, chatCode: 200}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/openai/v1/models":
			// PUBLIC, as the real one: answers whatever the key
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, catalogJSON)
		case r.Method == "GET" && r.URL.Path == "/v2/quotas":
			f.mu.Lock()
			code, body := f.quotaCode, f.quotas
			f.mu.Unlock()
			w.WriteHeader(code)
			io.WriteString(w, body)
		case r.Method == "POST" && r.URL.Path == "/openai/v1/chat/completions":
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.chatBody, f.chatAuth = b, r.Header.Get("Authorization")
			f.chatCalls++
			code, resp := f.chatCode, f.chatBodyR
			f.mu.Unlock()
			if code != 200 {
				w.WriteHeader(code)
				io.WriteString(w, resp)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if resp != "" {
				io.WriteString(w, resp)
				return
			}
			io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":10,"total_tokens":10,"completion_tokens":0}}`+"\n\n")
			io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"pong"}}],"usage":{"prompt_tokens":10,"total_tokens":11,"completion_tokens":1}}`+"\n\n")
			io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"total_tokens":11,"completion_tokens":1}}`+"\n\n")
			io.WriteString(w, `data: {"choices":[],"usage":{"prompt_tokens":10,"total_tokens":11,"completion_tokens":1}}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"API route not found: '`+r.URL.Path+`'. (HINT: Is the URL spelled correctly?)"}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *synthFake) provider(t *testing.T, key string) *Synthetic {
	t.Helper()
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	p := NewSynthetic()
	p.baseURL = f.URL + "/openai/v1"
	p.quotasURL = f.URL + "/v2/quotas"
	p.apiKey = key
	return p
}

func (f *synthFake) lastChat(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(f.chatBody, &m); err != nil {
		t.Fatalf("the last chat request is not JSON: %q", f.chatBody)
	}
	return m
}

func ask(t *testing.T, p *Synthetic, level string) (*types.Response, error) {
	t.Helper()
	return p.CompleteStream(context.Background(), &types.Request{
		Model: "hf:zai-org/GLM-4.7-Flash", MaxTokens: 100, ThinkingLevel: level,
		Messages: []types.Message{types.NewUserTextMessage("hi")},
	}, nil)
}

// ── what goes on the wire ───────────────────────────────────────────────────────────────────────────

func TestAChatRequestCarriesTheKeyAndTheEffort(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "syn_test_key")
	resp, err := ask(t, p, "off")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "pong" || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 1 {
		t.Errorf("response = %q usage %+v", resp.Text, resp.Usage)
	}
	if f.chatAuth != "Bearer syn_test_key" {
		t.Errorf("Authorization = %q", f.chatAuth)
	}
	body := f.lastChat(t)
	if body["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want none for the harness level off", body["reasoning_effort"])
	}
	if body["model"] != "hf:zai-org/GLM-4.7-Flash" {
		t.Errorf("model = %v", body["model"])
	}
	// none of the fields Synthetic ignores
	for _, ignored := range []string{"think", "thinking"} {
		if _, ok := body[ignored]; ok {
			t.Errorf("the request carries %q, which Synthetic ignores", ignored)
		}
	}
}

// The DeepSeek trap, as a regression test: the shared builder turns every level but "max" into "high" for a
// model whose id contains "deepseek", which is DeepSeek's OWN API's rule. hf:deepseek-ai/... on Synthetic
// accepts low and xhigh as themselves, so they must reach it unchanged.
func TestADeepSeekModelKeepsEveryLevelItWasAsked(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "k")
	for _, level := range []string{"low", "medium", "high", "xhigh", "max", "off"} {
		if _, err := p.CompleteStream(context.Background(), &types.Request{
			Model: "hf:deepseek-ai/DeepSeek-V4.1-Flash-1017", MaxTokens: 50, ThinkingLevel: level,
			Messages: []types.Message{types.NewUserTextMessage("hi")},
		}, nil); err != nil {
			t.Fatal(err)
		}
		body := f.lastChat(t)
		want := level
		if level == "off" {
			want = "none"
		}
		if body["reasoning_effort"] != want {
			t.Errorf("level %s reached a DeepSeek model as reasoning_effort=%v, want %s", level, body["reasoning_effort"], want)
		}
		if _, ok := body["thinking"]; ok {
			t.Errorf("level %s: the DeepSeek 'thinking' object was added", level)
		}
	}
}

// With no level chosen, nothing about reasoning is sent: the model's own default applies.
func TestNoLevelSendsNoReasoningField(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "k")
	if _, err := ask(t, p, ""); err != nil {
		t.Fatal(err)
	}
	body := f.lastChat(t)
	for _, k := range []string{"reasoning_effort", "think", "thinking"} {
		if _, ok := body[k]; ok {
			t.Errorf("%q was sent with no thinking level", k)
		}
	}
}

// The other OpenAI-dialect providers are untouched by Synthetic's opt-in: through the shared builder a
// DeepSeek model still gets the DeepSeek rule, and "off" still sends think:false.
func TestTheSharedBuilderStillTreatsOtherProvidersAsBefore(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := NewCustomOpenAI("other", types.CustomProvider{Type: "openai", URL: srv.URL})
	if _, err := c.CompleteStream(context.Background(), &types.Request{
		Model: "deepseek-x", ThinkingLevel: "low", Messages: []types.Message{types.NewUserTextMessage("hi")},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if gotBody["reasoning_effort"] != "high" || gotBody["think"] != true {
		t.Errorf("a custom DeepSeek provider changed: reasoning_effort=%v think=%v, want high / true", gotBody["reasoning_effort"], gotBody["think"])
	}
}

// ── key validation ───────────────────────────────────────────────────────────────────────────────────

// The catalog is public (it answers 200 with no key and with a bad one), so connecting proves the key with
// a real chat request. 401 is the ONLY verdict that means the key is wrong.
func TestTheKeyVerdictOfEveryStatus(t *testing.T) {
	for status, wantOK := range map[int]bool{
		200: true,
		402: true,  // no credits and no plan: the key is good, the account is empty
		429: true,  // too many concurrent requests: the key is good, retry later
		401: false, // invalid or missing key
		403: false,
		400: false, // the request itself was refused: not a verdict on the key, an error to show
		404: false,
		500: false,
		502: false,
	} {
		err := syntheticKeyVerdict(status, strings.NewReader(`{"error":"boom"}`))
		if (err == nil) != wantOK {
			t.Errorf("status %d: err = %v, want ok=%v", status, err, wantOK)
		}
	}
	if err := syntheticKeyVerdict(401, strings.NewReader(`{"error":"Invalid API Key."}`)); err == nil || err.Error() != "invalid API key" {
		t.Errorf("a 401 must read 'invalid API key', got %v", err)
	}
	// a 500 carries the provider's own text
	if err := syntheticKeyVerdict(500, strings.NewReader(`{"error":"upstream down"}`)); err == nil || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("a 500 lost the provider's text: %v", err)
	}
}

func TestConnectingWithAGoodKeyListsTheModelsAndStoresTheKey(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	if err := p.Connect(types.APIKeyCredentials("syn_good")); err != nil {
		t.Fatal(err)
	}
	if !p.IsActive() {
		t.Error("not active after Connect")
	}
	if got := len(p.Models()); got != 3 {
		t.Errorf("%d models, want the 3 hf: text models of the fixture", got)
	}
	if v, ok, _ := config.GetCredentialsManager().APIKey("synthetic"); !ok || v != "syn_good" {
		t.Errorf("stored key = %q (%v)", v, ok)
	}
	if f.chatAuth != "Bearer syn_good" {
		t.Errorf("the validation request used %q", f.chatAuth)
	}
	body := f.lastChat(t)
	if body["max_tokens"] != float64(1) || body["model"] != syntheticProbeModel {
		t.Errorf("the validation request is not a one-token request on the cheapest model: %v", body)
	}
}

// A rejected key is rolled back: not active, nothing stored.
func TestConnectingWithABadKeyRollsBack(t *testing.T) {
	f := newSynthFake(t)
	f.chatCode, f.chatBodyR = 401, `{"error":"Invalid API Key."}`
	p := f.provider(t, "")
	err := p.Connect(types.APIKeyCredentials("syn_bad"))
	if err == nil || !strings.Contains(err.Error(), "invalid API key") {
		t.Fatalf("Connect err = %v, want invalid API key", err)
	}
	if p.apiKey != "" {
		t.Error("the rejected key was kept on the provider")
	}
	if _, ok, _ := config.GetCredentialsManager().APIKey("synthetic"); ok {
		t.Error("the rejected key was stored")
	}
	if len(p.Models()) != 0 {
		t.Error("models were cached for a rejected key")
	}
}

// An account with no credits (402) or at its concurrency limit (429) still CONNECTS: the key is valid.
func TestAKeyOnAnEmptyOrBusyAccountStillConnects(t *testing.T) {
	for _, code := range []int{402, 429} {
		f := newSynthFake(t)
		f.chatCode, f.chatBodyR = code, `{"error":"x"}`
		p := f.provider(t, "")
		if err := p.Connect(types.APIKeyCredentials("syn_ok")); err != nil {
			t.Errorf("status %d: Connect failed: %v", code, err)
		}
		if len(p.Models()) == 0 {
			t.Errorf("status %d: connected but no models", code)
		}
	}
}

func TestConnectRejectsTheWrongCredentialKindAndAnEmptyKey(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	if err := p.Connect(types.Credentials{Type: types.CredTypeOAuth}); err == nil {
		t.Error("an OAuth credential was accepted")
	}
	if err := p.Connect(types.APIKeyCredentials("")); err == nil {
		t.Error("an empty key was accepted")
	}
	if f.chatCalls != 0 {
		t.Errorf("%d requests were made for credentials that are invalid on their face", f.chatCalls)
	}
}

func TestDisconnectForgetsTheKeyAndTheModels(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	if err := p.Connect(types.APIKeyCredentials("syn_ok")); err != nil {
		t.Fatal(err)
	}
	if err := p.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if p.apiKey != "" || len(p.Models()) != 0 {
		t.Errorf("still holding key %q and %d models", p.apiKey, len(p.Models()))
	}
	if _, ok, _ := config.GetCredentialsManager().APIKey("synthetic"); ok {
		t.Error("the stored key survived")
	}
}

// ── subscription ────────────────────────────────────────────────────────────────────────────────────

// Khan's rule: /v2/quotas with data (a "subscription" object) means a subscription; {} (this account:
// credits only) means pay-per-use.
func TestSubscriptionIsDecidedByTheQuotasBody(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want bool
	}{
		"the documented subscription":    {`{"subscription":{"limit":135,"requests":0,"renewsAt":"2025-09-21T14:36:14.288Z"}}`, true},
		"a subscription with extra keys": {`{"subscription":{"limit":1,"extra":true},"credits":{"balance":3}}`, true},
		"an empty subscription object":   {`{"subscription":{}}`, true},
		"empty (credits only)":           {`{}`, false},
		"subscription is null":           {`{"subscription":null}`, false},
		"subscription is a string":       {`{"subscription":"pro"}`, false},
		"subscription is a number":       {`{"subscription":1}`, false},
		"subscription is an array":       {`{"subscription":[]}`, false},
		"subscription is false":          {`{"subscription":false}`, false},
		"another key only":               {`{"credits":{"balance":3}}`, false},
		"a different case":               {`{"Subscription":{"limit":1}}`, false},
		"not JSON":                       {`<html>`, false},
		"empty body":                     {``, false},
		"a JSON array":                   {`[]`, false},
		"a JSON string":                  {`"subscription"`, false},
		"null":                           {`null`, false},
	} {
		if got := syntheticHasSubscription([]byte(c.body)); got != c.want {
			t.Errorf("%s (%q): got %v, want %v", name, c.body, got, c.want)
		}
	}
}

// End to end through the provider: the quotas answer marks every model, and any failure of the quotas call
// falls back to pay-per-use WITHOUT failing the connection.
func TestTheSubscriptionFlagOnTheModels(t *testing.T) {
	for name, c := range map[string]struct {
		code int
		body string
		want bool
	}{
		"a subscription":               {200, `{"subscription":{"limit":135,"requests":0,"renewsAt":"x"}}`, true},
		"credits only":                 {200, `{}`, false},
		"quotas 500":                   {500, `{"subscription":{"limit":1}}`, false}, // a body that LOOKS right behind an error status
		"quotas 401":                   {401, `{"error":"Invalid API Key."}`, false},
		"quotas 404":                   {404, `{"error":"not found"}`, false},
		"quotas 429":                   {429, `{"error":"Too many"}`, false},
		"quotas 200 not JSON":          {200, `oops`, false},
		"quotas 200 subscription null": {200, `{"subscription":null}`, false},
	} {
		f := newSynthFake(t)
		f.quotaCode, f.quotas = c.code, c.body
		p := f.provider(t, "")
		if err := p.Connect(types.APIKeyCredentials("syn_ok")); err != nil {
			t.Errorf("%s: Connect failed (the quotas call must never break a working key): %v", name, err)
			continue
		}
		for _, m := range p.Models() {
			if m.IsSubscription != c.want {
				t.Errorf("%s: %s IsSubscription = %v, want %v", name, m.ID, m.IsSubscription, c.want)
			}
		}
	}
}

// Unreachable quotas (the connection is refused) is also "not known": it must not fail Connect.
func TestAnUnreachableQuotasEndpointDoesNotBreakConnecting(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	p.quotasURL = "http://127.0.0.1:1/v2/quotas"
	if err := p.Connect(types.APIKeyCredentials("syn_ok")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for _, m := range p.Models() {
		if m.IsSubscription {
			t.Errorf("%s marked as a subscription though nothing said so", m.ID)
		}
	}
}

// The quotas request carries the key (it answers 401 without one) and it is a GET.
func TestTheQuotasRequestCarriesTheKey(t *testing.T) {
	var got http.Header
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, method = r.Header.Clone(), r.Method
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	syntheticIsSubscription("syn_k", srv.URL, http.DefaultClient)
	if method != "GET" || got.Get("Authorization") != "Bearer syn_k" {
		t.Errorf("quotas request: %s with Authorization %q", method, got.Get("Authorization"))
	}
}

// ── errors ──────────────────────────────────────────────────────────────────────────────────────────

// Synthetic answers {"error":"<plain string>"}. The user must read the provider's own sentence, not the raw
// JSON, and every status is a normal error (nothing retries a 429).
func TestTheProvidersOwnSentenceIsWhatTheUserReads(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{402, `{"error":"Insufficient credits and no active subscription. Go to https://synthetic.new/billing to subscribe or https://dev.synthetic.new/usage purchase credits."}`, "Insufficient credits and no active subscription"},
		{429, `{"error":"Too many concurrent requests"}`, "Too many concurrent requests"},
		{401, `{"error":"Invalid API Key."}`, "Invalid API Key."},
		{404, `{"error":"hf:nobody/NoSuchModel is no longer supported. Try using a different model, like hf:moonshotai/Kimi-K3.1-Pro"}`, "is no longer supported"},
		{400, `{"error":"Your model name should start with an hf: prefix; for example: \"hf:zai-org/GLM-4.7-Flash\""}`, "should start with an hf: prefix"},
		{400, `{"error":"Model hf:zai-org/GLM-4.7-Flash does not appear to support image inputs."}`, "does not appear to support image inputs"},
		{400, `{"error":"Request failed validation:\nobject did not match any option in the schema."}`, "Request failed validation"},
	} {
		f := newSynthFake(t)
		f.chatCode, f.chatBodyR = c.status, c.body
		p := f.provider(t, "k")
		_, err := ask(t, p, "")
		if err == nil {
			t.Errorf("status %d: no error", c.status)
			continue
		}
		var apiErr *types.ProviderAPIError
		if !asAPIError(err, &apiErr) || apiErr.Status != c.status {
			t.Errorf("status %d: error %v is not a ProviderAPIError with that status", c.status, err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, c.want) {
			t.Errorf("status %d: the user would read %q, which lacks %q", c.status, msg, c.want)
		}
		if strings.Contains(msg, `{"error"`) {
			t.Errorf("status %d: the raw JSON leaked into the message: %q", c.status, msg)
		}
		if !strings.HasPrefix(msg, "synthetic API error") {
			t.Errorf("status %d: the message does not name the provider: %q", c.status, msg)
		}
	}
}

func asAPIError(err error, target **types.ProviderAPIError) bool {
	for e := err; e != nil; {
		if a, ok := e.(*types.ProviderAPIError); ok {
			*target = a
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// An error body in OpenAI's own shape, or no body, or a body that is not JSON, still produces an error with
// something in it: the extraction only ever IMPROVES the message.
func TestErrorTextExtractionDegradesGracefully(t *testing.T) {
	for in, want := range map[string]string{
		`{"error":"plain"}`:                  "plain",
		`{"error":"  padded  "}`:             "padded",
		`{"error":{"message":"objectform"}}`: "objectform",
		`{"error":{"code":1}}`:               "",
		`{"error":null}`:                     "",
		`{"error":""}`:                       "",
		`{"error":7}`:                        "",
		`{}`:                                 "",
		`not json`:                           "",
		``:                                   "",
		`[]`:                                 "",
	} {
		if got := syntheticErrorText([]byte(in)); got != want {
			t.Errorf("syntheticErrorText(%q) = %q, want %q", in, got, want)
		}
	}
	for _, body := range []string{`<html>bad gateway</html>`, ``, `{"error":{"code":1}}`} {
		err := syntheticAPIError(502, []byte(body))
		if err == nil || !strings.Contains(err.Error(), "502") {
			t.Errorf("body %q: err = %v, want a 502 error", body, err)
		}
	}
}

// The provider's sentence is untrusted text that ends up on a terminal: control characters, ESC above all,
// are removed on the way (types.NewProviderAPIError does it; this pins that Synthetic's path goes through it).
func TestTheProvidersSentenceIsCleanedOfControlCharacters(t *testing.T) {
	err := syntheticAPIError(402, []byte("{\"error\":\"pay \\u001b]0;pwned\\u0007 now\"}"))
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("a control character reached the message: %q", err.Error())
	}
}

// ── streaming ────────────────────────────────────────────────────────────────────────────────────────

// What Synthetic really streams, in order: an empty first chunk, reasoning in delta.reasoning, content,
// a finish chunk, and a final usage-only chunk (choices: []), usage on EVERY chunk and cumulative.
func TestTheRealStreamShapeIsParsed(t *testing.T) {
	f := newSynthFake(t)
	f.chatBodyR = strings.Join([]string{
		`data: {"id":"chatcmpl-a","object":"chat.completion.chunk","model":"zai-org/GLM-4.7-Flash","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}],"usage":{"prompt_tokens":10,"total_tokens":10,"completion_tokens":0},"prompt_token_ids":null}`,
		`data: {"id":"chatcmpl-a","choices":[{"index":0,"delta":{"reasoning":"Let me"},"logprobs":null,"finish_reason":null,"token_ids":null}],"usage":{"prompt_tokens":10,"total_tokens":12,"completion_tokens":2}}`,
		`data: {"id":"chatcmpl-a","choices":[{"index":0,"delta":{"reasoning":" think."},"finish_reason":null}],"usage":{"prompt_tokens":10,"total_tokens":14,"completion_tokens":4}}`,
		`data: {"id":"chatcmpl-a","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}],"usage":{"prompt_tokens":10,"total_tokens":15,"completion_tokens":5}}`,
		`data: {"id":"chatcmpl-a","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop","stop_reason":154827,"token_ids":null}],"usage":{"prompt_tokens":10,"total_tokens":15,"completion_tokens":5}}`,
		`data: {"id":"chatcmpl-a","choices":[],"usage":{"prompt_tokens":10,"total_tokens":15,"completion_tokens":5}}`,
		`data: [DONE]`, ``,
	}, "\n\n")
	p := f.provider(t, "k")
	var thinking, text string
	resp, err := p.CompleteStream(context.Background(), &types.Request{
		Model: "hf:zai-org/GLM-4.7-Flash", MaxTokens: 50, ThinkingLevel: "low",
		Messages: []types.Message{types.NewUserTextMessage("hi")},
	}, func(e types.StreamEvent) {
		switch e.Type {
		case types.StreamThinkingDelta:
			thinking += e.Delta
		case types.StreamTextDelta:
			text += e.Delta
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if thinking != "Let me think." || text != "pong" || resp.Text != "pong" {
		t.Errorf("thinking=%q text=%q resp=%q", thinking, text, resp.Text)
	}
	// the usage is the LAST cumulative object, not a sum of the chunks
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v, want 10 in / 5 out (the last chunk's, not summed)", resp.Usage)
	}
}

// The cache. Synthetic reports prompt_tokens as the WHOLE prompt and cached_tokens as part of it; the
// session adds fresh + cache read, so the cached part must be taken out of the fresh count or it is billed
// twice. (Measured: prompt_tokens 7015, cached_tokens 6976.)
func TestACachedPromptIsAccountedOnce(t *testing.T) {
	f := newSynthFake(t)
	f.chatBodyR = `data: {"choices":[{"index":0,"delta":{"content":"OK"}}],"usage":{"prompt_tokens":7015,"total_tokens":7017,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6976}}}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":7015,"total_tokens":7017,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6976}}}` + "\n\n" +
		"data: [DONE]\n\n"
	p := f.provider(t, "k")
	var ev types.StreamEvent
	resp, err := p.CompleteStream(context.Background(), &types.Request{
		Model: "hf:zai-org/GLM-4.7-Flash", MaxTokens: 5, Messages: []types.Message{types.NewUserTextMessage("hi")},
	}, func(e types.StreamEvent) {
		if e.Type == types.StreamUsage {
			ev = e
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 39 || resp.Usage.CacheRead != 6976 {
		t.Errorf("usage = %+v, want 39 fresh + 6976 cache read", resp.Usage)
	}
	if ev.InputTokens != 39 || ev.CacheRead != 6976 || ev.OutputTokens != 2 {
		t.Errorf("the usage event the session reads = %+v", ev)
	}
}

// Tool calls: the opener carries id, type and name with empty arguments, then fragments by index, and two
// parallel calls arrive one after the other. Synthetic's ids are chatcmpl-tool-<hex>, not call_*.
func TestStreamedToolCallsAreAssembled(t *testing.T) {
	f := newSynthFake(t)
	f.chatBodyR = strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":183,"completion_tokens":0}}`,
		`data: {"choices":[{"index":0,"delta":{"content":"I'll check both."}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-b3ef911950411d54","type":"function","index":0,"function":{"name":"get_weather","arguments":""}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\""}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"Paris"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-9b9edac93aa13ddd","type":"function","index":1,"function":{"name":"get_weather","arguments":""}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"city\":\"Tok"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"yo\"}"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":183,"completion_tokens":97,"prompt_tokens_details":{"cached_tokens":128}}}`,
		`data: [DONE]`, ``,
	}, "\n\n")
	p := f.provider(t, "k")
	resp, err := p.CompleteStream(context.Background(), &types.Request{
		Model: "hf:zai-org/GLM-4.7-Flash", MaxTokens: 100, Messages: []types.Message{types.NewUserTextMessage("weather?")},
		Tools: []types.ToolDef{{Name: "get_weather", Description: "w", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("%d tool calls, want 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	args := map[string]bool{}
	for _, tc := range resp.ToolCalls {
		if tc.Name != "get_weather" || tc.ID == "" {
			t.Errorf("tool call %+v", tc)
		}
		args[string(tc.Input)] = true
	}
	if !args[`{"city":"Paris"}`] || !args[`{"city":"Tokyo"}`] {
		t.Errorf("arguments = %v, want Paris and Tokyo assembled from their fragments", args)
	}
	if resp.Usage.InputTokens != 55 || resp.Usage.CacheRead != 128 {
		t.Errorf("usage = %+v, want 55 fresh + 128 cached", resp.Usage)
	}
}

// ── registration ─────────────────────────────────────────────────────────────────────────────────────

func TestSyntheticIsABuiltInProviderOfTheRightShape(t *testing.T) {
	var got Provider
	for _, p := range All() {
		if p.Name() == "synthetic" {
			got = p
		}
	}
	if got == nil {
		t.Fatal("synthetic is not in the registry")
	}
	if got.DisplayName() != "Synthetic" || got.Dialect() != DialectOpenAI || got.CredentialType() != types.CredTypeAPIKey {
		t.Errorf("synthetic = %q dialect %q credentials %q", got.DisplayName(), got.Dialect(), got.CredentialType())
	}
}

// A user-declared provider cannot take the name of a built-in.
func TestTheNameSyntheticIsReserved(t *testing.T) {
	if !config.IsReservedProviderName("synthetic") {
		t.Error("synthetic is not a reserved provider name")
	}
}

func TestTheEnvironmentVariableActivatesIt(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	t.Setenv("SYNTHETIC_API_KEY", "syn_from_env")
	p := NewSynthetic()
	if !p.IsActive() || p.ActivationSource() != ActivationEnvVar {
		t.Errorf("active=%v source=%q, want active via the environment", p.IsActive(), p.ActivationSource())
	}
	creds, err := p.ResolveCredentials()
	if err != nil || creds.APIKey != "syn_from_env" {
		t.Errorf("credentials = %+v, %v", creds, err)
	}
	t.Setenv("SYNTHETIC_API_KEY", "")
	if q := NewSynthetic(); q.IsActive() {
		t.Error("active with no key anywhere")
	}
}

// The proxy: the key is added by harness and the paths are the OpenAI-style allowlist.
func TestTheUpstreamForTheProxy(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	p := NewSynthetic()
	if _, err := p.Upstream(); err == nil {
		t.Error("an upstream without a key")
	}
	p.apiKey = "syn_k"
	up, err := p.Upstream()
	if err != nil {
		t.Fatal(err)
	}
	if up.BaseURL != "https://api.synthetic.new/openai/v1" || up.Headers["Authorization"] != "Bearer syn_k" {
		t.Errorf("upstream = %q / %v", up.BaseURL, up.Headers)
	}
	if !up.Allowed("POST", "/chat/completions") || !up.Allowed("GET", "/models") || up.Allowed("POST", "/embeddings") {
		t.Error("the proxy allowlist is not the OpenAI chat one")
	}
}

// Disconnect must be idempotent and never panic on a provider that was never connected.
func TestDisconnectingTwiceIsHarmless(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	p := NewSynthetic()
	for i := 0; i < 2; i++ {
		if err := p.Disconnect(); err != nil {
			t.Errorf("Disconnect #%d: %v", i+1, err)
		}
	}
}

func TestModelMetaOfAnUnknownModelIsNil(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	if err := p.Connect(types.APIKeyCredentials("k")); err != nil {
		t.Fatal(err)
	}
	if p.ModelMeta("hf:zai-org/GLM-4.7-Flash") == nil {
		t.Error("a listed model has no metadata")
	}
	if p.ModelMeta("syn:small:text") != nil || p.ModelMeta("nope") != nil {
		t.Error("an unlisted model has metadata")
	}
	// the copy is not shared with the cache
	m := p.ModelMeta("hf:zai-org/GLM-4.7-Flash")
	m.DisplayName = "changed"
	if p.ModelMeta("hf:zai-org/GLM-4.7-Flash").DisplayName == "changed" {
		t.Error("ModelMeta hands out the cache's own value")
	}
}

var _ = fmt.Sprint

// ── gaps found by mutation testing ──────────────────────────────────────────────────────────────────

// A 401 AND a 403 both read "invalid API key": the verdict is about the key, and the message must say so
// for either (a 403 that fell through to the generic error would tell the user the provider failed).
func TestBothRefusalsOfAKeyReadAsAnInvalidKey(t *testing.T) {
	for _, status := range []int{401, 403} {
		err := syntheticKeyVerdict(status, strings.NewReader(`{"error":"nope"}`))
		if err == nil || err.Error() != "invalid API key" {
			t.Errorf("status %d: err = %v, want exactly 'invalid API key'", status, err)
		}
	}
}

// The credential-kind check must hold on its own: an OAuth credential that CARRIES a key is still refused
// (the empty-key check would not catch it), and no request is made for it.
func TestAnOAuthCredentialWithAKeyIsStillRefused(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	err := p.Connect(types.Credentials{Type: types.CredTypeOAuth, APIKey: "syn_looks_valid"})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("Connect err = %v, want a refusal that names api_key", err)
	}
	if f.chatCalls != 0 || p.apiKey != "" {
		t.Errorf("%d requests were made and the key %q was kept for a credential of the wrong kind", f.chatCalls, p.apiKey)
	}
	if _, ok, _ := config.GetCredentialsManager().APIKey("synthetic"); ok {
		t.Error("a credential of the wrong kind was stored")
	}
}

// The provider name an OpenAI-dialect provider reports in an error is "openai" when it sets none, as it
// always was: the shared stream path must not rename every other provider's errors.
func TestAnOpenAIDialectProviderWithoutANameStillSaysOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
	}))
	defer srv.Close()
	c := NewCustomOpenAI("somebody", types.CustomProvider{Type: "openai", URL: srv.URL})
	_, err := c.CompleteStream(context.Background(), &types.Request{Model: "m", Messages: []types.Message{types.NewUserTextMessage("hi")}}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "openai API error 429") {
		t.Errorf("err = %v, want it to start with 'openai API error 429'", err)
	}
	if !strings.Contains(err.Error(), "slow down") {
		t.Errorf("the body was lost: %v", err)
	}
}

// Synthetic is offered where models are grouped for display, after opencode-go and before minimax.
func TestSyntheticAppearsInTheModelGroups(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	if err := p.Connect(types.APIKeyCredentials("syn_ok")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetForTest([]Provider{p}))
	groups := GetModelGroups("")
	if len(groups) != 1 || groups[0].Label != "Synthetic" || len(groups[0].Models) != 3 {
		t.Fatalf("groups = %+v, want one 'Synthetic' group with 3 models", groups)
	}
	for _, m := range groups[0].Models {
		if m.Provider != "synthetic" || !strings.HasPrefix(m.ID, "hf:") {
			t.Errorf("model %+v", m)
		}
	}
	// the active model is marked by its full name provider/id, with the id's own "/" and ":" intact
	groups = GetModelGroups("synthetic/hf:zai-org/GLM-4.7-Flash")
	active := 0
	for _, m := range groups[0].Models {
		if m.Active {
			active++
			if m.ID != "hf:zai-org/GLM-4.7-Flash" {
				t.Errorf("the active model is %q", m.ID)
			}
		}
	}
	if active != 1 {
		t.Errorf("%d active models, want exactly the one named", active)
	}
}

// A model id with a ":" and a second "/" must survive the provider/model split harness uses everywhere:
// the split is on the FIRST "/", so "synthetic/hf:zai-org/GLM-4.7-Flash" is provider "synthetic" and model
// "hf:zai-org/GLM-4.7-Flash".
func TestAnHFModelIdSurvivesTheProviderModelSplit(t *testing.T) {
	f := newSynthFake(t)
	p := f.provider(t, "")
	if err := p.Connect(types.APIKeyCredentials("syn_ok")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetForTest([]Provider{p}))
	got, model, err := Resolve("synthetic/hf:moonshotai/Kimi-K3.1-Pro")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name() != "synthetic" || model != "hf:moonshotai/Kimi-K3.1-Pro" {
		t.Errorf("resolved to provider %q model %q", got.Name(), model)
	}
	for _, bad := range []string{"synthetic/syn:small:text", "synthetic/infinite:small:vision", "synthetic/hf:nobody/Nothing"} {
		if _, _, err := Resolve(bad); err == nil || !IsUnavailable(err) {
			t.Errorf("Resolve(%q) = %v, want a 'model not available' error (not offered)", bad, err)
		}
	}
}
