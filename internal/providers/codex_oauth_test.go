package providers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/oauthflow"
	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// ── JWT account-id extraction (shared with oauthflow's own tests) ─────────

// makeIDToken builds a fake id_token JWT with the given claims map as its
// payload (header/signature are filler — ExtractChatGPTAccountID only decodes
// the payload).
func makeIDToken(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fakesig"
}

func TestCodexModelMetadataKeepsProviderContextAuthoritative(t *testing.T) {
	// The Codex endpoint reports 272k for Luna. EnrichMeta must not replace
	// that authoritative value with OpenRouter's 1.05M deployment metadata.
	meta := llm.EnrichMeta(types.ModelMeta{
		ID:            "gpt-5.6-luna",
		DisplayName:   "GPT-5.6-Luna",
		ContextWindow: 272000,
	})
	if meta.ContextWindow != 272000 {
		t.Fatalf("context window = %d, want provider-authoritative 272000", meta.ContextWindow)
	}
}

// ── Request translation ───────────────────────────────────────────────────

func TestBuildCodexRequestHoistsSystemPromptToInstructions(t *testing.T) {
	req := &types.Request{
		Model:        "gpt-5.4-codex",
		SystemPrompt: "You are a focused sub-agent.",
		Messages: []types.Message{
			types.NewUserTextMessage("do the thing"),
		},
	}
	out, err := buildCodexRequest(req, "sess-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Instructions != "You are a focused sub-agent." {
		t.Errorf("instructions = %q, want the system prompt hoisted verbatim", out.Instructions)
	}
	// No system message may appear as an input item — the backend requires
	// system content to travel in `instructions` only.
	for _, item := range out.Input {
		if item.Type == "message" && item.Role == "system" {
			t.Errorf("system message leaked into input items — must be hoisted to instructions only")
		}
	}
}

func TestBuildCodexRequestMandatoryWireFields(t *testing.T) {
	out, err := buildCodexRequest(&types.Request{Model: "gpt-5.4-codex"}, "sess")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Stream {
		t.Error("stream must always be true")
	}
	if out.Store {
		t.Error("store must always be false — the backend rejects store:true outright")
	}
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v, want exactly [reasoning.encrypted_content]", out.Include)
	}
	if out.PromptCacheKey != "sess" {
		t.Errorf("prompt_cache_key = %q, want the stable session id (server-side prompt-cache routing)", out.PromptCacheKey)
	}
}

func TestBuildCodexRequestOmitsMaxOutputTokens(t *testing.T) {
	// The Codex backend REJECTS max_output_tokens (unsupported parameter) —
	// so the codexRequest struct has no such field at all, and MaxTokens
	// must be silently dropped. This test pins that: marshal the request
	// and assert the field name never appears in the wire body.
	out, err := buildCodexRequest(&types.Request{
		Model:     "gpt-5.4-codex",
		MaxTokens: 4096,
	}, "sess")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wire, _ := json.Marshal(out)
	if strings.Contains(string(wire), "max_output_tokens") || strings.Contains(string(wire), "max_tokens") {
		t.Errorf("wire request contains a token limit the backend rejects: %s", wire)
	}
}

func TestBuildCodexRequestThinkingLevels(t *testing.T) {
	cases := []struct {
		level string
		want  string // "" = field omitted
	}{
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "high"}, // OpenAI has no xhigh — clamped
		{"off", ""},       // omitted — backend picks the model default
		{"", ""},          // unset — same
	}
	for _, c := range cases {
		out, err := buildCodexRequest(&types.Request{Model: "gpt-5.4-codex", ThinkingLevel: c.level}, "sess")
		if err != nil {
			t.Fatalf("level %q: unexpected error: %v", c.level, err)
		}
		got := ""
		if out.Reasoning != nil {
			got = out.Reasoning.Effort
		}
		if got != c.want {
			t.Errorf("thinking level %q: reasoning.effort = %q, want %q", c.level, got, c.want)
		}
	}
}

func TestBuildCodexRequestFlatTools(t *testing.T) {
	out, err := buildCodexRequest(&types.Request{
		Model: "gpt-5.4-codex",
		Tools: []types.ToolDef{
			{Name: "Bash", Description: "Run a command", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}, "sess")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(out.Tools))
	}
	tool := out.Tools[0]
	if tool.Type != "function" || tool.Name != "Bash" {
		t.Errorf("tool shape wrong: %+v — want flat {type:function,name:B,...}, no function nesting", tool)
	}
	// Round-trip the wire form: the flat shape means "name" is a TOP-LEVEL
	// key of the tool object, not nested under "function".
	var probe map[string]any
	if err := json.Unmarshal(toolJSON(t, tool), &probe); err != nil {
		t.Fatal(err)
	}
	fn, ok := probe["function"]
	if ok {
		t.Errorf("tool is nested under a 'function' key (Chat Completions shape) — the Codex backend wants flat: %v", fn)
	}
	if probe["name"] != "Bash" || probe["parameters"] == nil {
		t.Errorf("tool name/parameters must be top-level: %v", probe)
	}
	if out.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q, want auto when tools are present", out.ToolChoice)
	}
	if out.ParallelToolCalls == nil || !*out.ParallelToolCalls {
		t.Error("parallel_tool_calls must be true when tools are present")
	}
}

// ── Input translation ─────────────────────────────────────────────────────

func TestBuildCodexInputMessageKinds(t *testing.T) {
	input, err := buildCodexInput([]types.Message{
		types.NewUserTextMessage("first user turn"),
		types.NewAssistantToolCallMessage("", "", "", []types.ToolCall{
			{ID: "call_abc", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
		}),
		{Role: types.RoleUser, Parts: []types.ContentPart{{
			ToolResult: &types.ToolResult{ID: "call_abc", Output: "file.txt"},
		}}},
		types.NewAssistantTextMessage("all done"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var kinds []string
	for _, it := range input {
		kinds = append(kinds, it.Type)
	}
	want := []string{"message", "function_call", "function_call_output", "message"}
	if len(kinds) != len(want) {
		t.Fatalf("input item types = %v, want %v", kinds, want)
	}
	for i, w := range want {
		if kinds[i] != w {
			t.Errorf("item %d: type = %q, want %q", i, kinds[i], w)
		}
	}
	// Function call pairing: the call_id must be echoed exactly — no re-keying.
	if input[1].CallID != "call_abc" || input[1].Name != "Bash" {
		t.Errorf("function_call = %+v, want call_id=call_abc name=Bash", input[1])
	}
	if input[2].CallID != "call_abc" || input[2].Output != "file.txt" {
		t.Errorf("function_call_output = %+v, want call_id=call_abc output=file.txt", input[2])
	}
	// Assistant text content must use output_text; user input_text.
	if input[3].Content[0].Type != "output_text" {
		t.Errorf("assistant message content type = %q, want output_text", input[3].Content[0].Type)
	}
	if input[0].Content[0].Type != "input_text" {
		t.Errorf("user message content type = %q, want input_text", input[0].Content[0].Type)
	}
}

// ── SSE parsing — the Codex Responses dialect ─────────────────────────────

// codexSSEFixture is a minimal full-stream fixture: text deltas, a tool
// call, then the completed envelope with usage. Each event is terminated by
// a BLANK line — ParseSSE concatenates consecutive "data:" lines into ONE
// event (multi-line data is legal SSE), so a missing blank line between
// events would glue them together.
func codexSSEFixture() string {
	return strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hello "}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"world"}`,
		``,
		`data: {"type":"response.reasoning_text.delta","delta":"thinking..."}`,
		``,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_item1","call_id":"call_xyz","name":"Bash","arguments":""}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_item1","delta":"{\"command\":"}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_item1","delta":"\"echo hi\"}"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_item1","call_id":"call_xyz","name":"Bash","arguments":"{\"command\":\"echo hi\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":123,"output_tokens":45,"input_tokens_details":{"cached_tokens":100}}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
}

func TestParseCodexStreamFullDialect(t *testing.T) {
	var events []types.StreamEvent
	resp, err := parseCodexStream(t.Context(), strings.NewReader(codexSSEFixture()), func(e types.StreamEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text != "hello world" {
		t.Errorf("text = %q, want %q", resp.Text, "hello world")
	}
	if resp.Usage.InputTokens != 123 || resp.Usage.OutputTokens != 45 || resp.Usage.CacheRead != 100 {
		t.Errorf("usage = %+v, want in=123 out=45 cacheRead=100", resp.Usage)
	}
	// The ReAct loop executes tools from resp.ToolCalls (the RETURNED
	// response) — the events are only the transports' rendering feed. A
	// function_call turn that fails to populate resp.ToolCalls persists an
	// empty assistant message and executes nothing (the exact live bug).
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("resp.ToolCalls = %d, want 1 (the ReAct loop needs it to execute the call)", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "Bash" {
		t.Errorf("tool name = %q, want Bash", resp.ToolCalls[0].Name)
	}
	if string(resp.ToolCalls[0].Input) != `{"command":"echo hi"}` {
		t.Errorf("tool input = %s", resp.ToolCalls[0].Input)
	}

	// Three-phase tool event flow: Start → Delta(s) → End, ALL carrying the
	// SAME canonical tool id (the TUI pairs its block by that id). A missing
	// Start/Delta meant the TUI never rendered the tool header or streaming
	// args during execution (the live bug Gus reported).
	var startIdx, endIdx = -1, -1
	var deltaCount int
	var startID, endID string
	for i, e := range events {
		switch e.Type {
		case types.StreamToolStart:
			startIdx, startID = i, e.ToolID
		case types.StreamToolDelta:
			deltaCount++
		case types.StreamToolEnd:
			endIdx, endID = i, e.ToolID
		}
	}
	if startIdx == -1 {
		t.Fatal("no StreamToolStart event — the TUI can't render the tool header")
	}
	if endIdx == -1 {
		t.Fatal("no StreamToolEnd event")
	}
	if deltaCount < 2 {
		t.Errorf("expected ≥2 StreamToolDelta events (one per arguments chunk), got %d", deltaCount)
	}
	if startID != endID {
		t.Errorf("tool id mismatch: Start=%q End=%q — the TUI pairs blocks by this id", startID, endID)
	}
	if startIdx > endIdx {
		t.Errorf("Start (idx %d) must precede End (idx %d)", startIdx, endIdx)
	}
	var sawToolEnd bool
	for _, e := range events {
		if e.Type == types.StreamToolEnd {
			if e.ToolName != "Bash" {
				t.Errorf("tool name = %q, want Bash", e.ToolName)
			}
			if string(e.ToolArgs) != `{"command":"echo hi"}` {
				t.Errorf("tool args = %s, want the fixture's arguments verbatim", e.ToolArgs)
			}
			if e.ToolID == "" {
				t.Error("tool event must carry a canonical (toolu_) ID")
			}
			sawToolEnd = true
		}
	}
	if !sawToolEnd {
		t.Error("no StreamToolEnd event emitted for the function_call output_item")
	}
}

func TestParseCodexStreamDroppedConnectionIsAnError(t *testing.T) {
	// Stream closes WITHOUT response.completed — must be an error (the
	// v0.76.53 dropped-connection contract), never a silent partial success.
	_, err := parseCodexStream(t.Context(), strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"half\"}\n\n"), nil)
	if err == nil {
		t.Fatal("expected an error for a stream that closed before response.completed")
	}
	if !strings.Contains(err.Error(), "response.completed") {
		t.Errorf("error should mention the missing marker, got: %v", err)
	}
}

func TestParseCodexStreamResponseFailedIsAnError(t *testing.T) {
	_, err := parseCodexStream(t.Context(), strings.NewReader(
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"over quota\"}}}\n\n"), nil)
	if err == nil {
		t.Fatal("expected an error for response.failed")
	}
	if !strings.Contains(err.Error(), "codex response failed") {
		t.Errorf("error should identify the failure, got: %v", err)
	}
}

// ── Account-id extraction fallbacks (Zed's three-level chain) ─────────────

func TestExtractChatGPTAccountIDFallbacks(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"top-level claim", map[string]any{"chatgpt_account_id": "acct-top"}, "acct-top"},
		{"namespaced auth claim", map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-ns"},
		}, "acct-ns"},
		{"organizations fallback", map[string]any{
			"organizations": []any{map[string]any{"id": "acct-org"}},
		}, "acct-org"},
		{"all three — top-level wins", map[string]any{
			"chatgpt_account_id":          "acct-top",
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-ns"},
			"organizations":               []any{map[string]any{"id": "acct-org"}},
		}, "acct-top"},
		{"none present", map[string]any{"sub": "user-1"}, ""},
	}
	for _, tc := range tests {
		payload, _ := json.Marshal(tc.claims)
		idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
			base64.RawURLEncoding.EncodeToString(payload) + ".sig"
		if got := oauthflow.ExtractChatGPTAccountID(idToken); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// Malformed/empty tokens extract to "".
	if got := oauthflow.ExtractChatGPTAccountID(""); got != "" {
		t.Errorf("empty id_token should give empty account id, got %q", got)
	}
	if got := oauthflow.ExtractChatGPTAccountID("not-a-jwt"); got != "" {
		t.Errorf("malformed id_token should give empty account id, got %q", got)
	}
}

func toolJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ── Reasoning replay — the encrypted_content contract ─────────────────────

// TestBuildCodexInputReasoningReplayRequiresEncryptedContent is the
// regression test for the 400 Gus hit live ("Unknown parameter:
// 'input[0].summary'") on gpt-5.6-luna: replaying a synthetic reasoning
// item (summary:[] with no real encrypted_content) is rejected — the backend
// VERIFIES the opaque encrypted_content it issued, so the only valid replay
// carries the genuine payload (round-tripped in ThinkingPart.Signature by
// parseCodexStream). No token → the reasoning item is dropped entirely.
func TestBuildCodexInputReasoningReplayRequiresEncryptedContent(t *testing.T) {
	withToken := &types.Message{
		Role: types.RoleAssistant,
		Parts: []types.ContentPart{
			{Thinking: &types.ThinkingPart{
				Content:      "internal reasoning",
				Signature:    "real-opaque-encrypted-payload",
				OpenAIItemID: "rs_original_item_id",
			}},
			{Text: "answer"},
		},
	}
	// Half-pair (encrypted content but NO item id) is ALSO invalid — the
	// backend verifies the payload against the id it was issued for
	// ("Encrypted content item_id did not match the target item id").
	halfPair := &types.Message{
		Role: types.RoleAssistant,
		Parts: []types.ContentPart{
			{Thinking: &types.ThinkingPart{Content: "internal reasoning", Signature: "real-opaque-encrypted-payload"}},
			{Text: "answer"},
		},
	}
	withoutToken := &types.Message{
		Role: types.RoleAssistant,
		Parts: []types.ContentPart{
			{Thinking: &types.ThinkingPart{Content: "internal reasoning"}},
			{Text: "answer"},
		},
	}

	// With a real token: the reasoning item IS replayed, carrying the opaque
	// payload and summary:[]. Assertions run on the WIRE JSON (the level
	// where the fat-struct bug lived: a plain `summary` field without
	// omitempty serialized "summary":null into EVERY input item — message
	// items included — which the backend rejects as unknown_parameter).
	input, err := buildCodexInput([]types.Message{*withToken})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	wireStr := string(wire)
	if !strings.Contains(wireStr, `"encrypted_content":"real-opaque-encrypted-payload"`) {
		t.Errorf("wire missing the round-tripped encrypted_content: %s", wireStr)
	}
	if !strings.Contains(wireStr, `"id":"rs_original_item_id"`) {
		t.Errorf("wire missing the ORIGINAL backend-issued item id (the payload is bound to it): %s", wireStr)
	}
	if !strings.Contains(wireStr, `"summary":[]`) {
		t.Errorf("wire missing summary:[] on the reasoning item (400 missing_required_parameter without it): %s", wireStr)
	}

	// Without a token: NO reasoning item at all, AND no summary key anywhere
	// in the wire (a plain message item must never carry it).
	input2, err := buildCodexInput([]types.Message{*withoutToken})
	if err != nil {
		t.Fatal(err)
	}
	wire2, _ := json.Marshal(input2)
	if strings.Contains(string(wire2), "reasoning") {
		t.Errorf("synthetic reasoning item replayed without encrypted_content — the exact live-400 shape: %s", wire2)
	}
	if strings.Contains(string(wire2), "summary") {
		t.Errorf("summary leaked into non-reasoning items (fat-struct bug): %s", wire2)
	}

	// Half-pair (encrypted content WITHOUT its item id): also dropped — the
	// backend binds the payload to the id, so a half-pair can never verify.
	input3, err := buildCodexInput([]types.Message{*halfPair})
	if err != nil {
		t.Fatal(err)
	}
	wire3, _ := json.Marshal(input3)
	if strings.Contains(string(wire3), "reasoning") {
		t.Errorf("reasoning item replayed without its item id — the backend binds encrypted_content to that id: %s", wire3)
	}
}

// TestBuildCodexInputPlainMessageNeverCarriesSummary is THE regression test
// for the live 400 Gus hit ("Unknown parameter: 'input[0].summary'"): a
// single plain user message must serialize WITHOUT any summary key — the
// fat-struct shape serialized "summary":null into every input item, which
// the backend rejects on non-reasoning items.
func TestBuildCodexInputPlainMessageNeverCarriesSummary(t *testing.T) {
	input, err := buildCodexInput([]types.Message{types.NewUserTextMessage("hola")})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "summary") {
		t.Errorf("a plain user message must never serialize a summary key — backend rejects it as unknown_parameter: %s", wire)
	}
	// And the full request wire too (the exact body the HTTP POST sends).
	out, err := buildCodexRequest(&types.Request{
		Model:    "gpt-5.6-luna",
		Messages: []types.Message{types.NewUserTextMessage("hola")},
	}, "sess")
	if err != nil {
		t.Fatal(err)
	}
	full, _ := json.Marshal(out)
	if strings.Contains(string(full), "summary") {
		t.Errorf("the full request wire carries a summary key on a non-reasoning input item: %s", full)
	}
}
