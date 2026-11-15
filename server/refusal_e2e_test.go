package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
)

// The whole path against a (fake) Anthropic that answers with the refusal stream
// it really sent in the field incident: custom `anthropic` provider → session →
// HTTP /ask. The turn fails with a clear error — one request, no retries — and the
// failed turn leaves no assistant message; then the SAME session is moved to
// another provider and the conversation goes on, which is what the hint promises.
func TestAnthropicRefusalEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var anthropicCalls atomic.Int32
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // model discovery
			w.Write([]byte(`{"data":[{"id":"claude-fake","display_name":"x","max_input_tokens":200000,"max_tokens":8192}],"has_more":false}`))
			return
		}
		anthropicCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"x","usage":{"input_tokens":17168,"output_tokens":0}}}`+"\n\n")
		fmt.Fprint(w, "event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"Cyber safeguards flagged this message. If this is legitimate security work, you can apply for the Cyber Verification Program: https://example.test/cvp","cyber_verification":{"type":"inactive"}}},"usage":{"input_tokens":17168,"output_tokens":0}}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer anth.Close()

	var otherSawHistory atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		roles := make([]string, 0, len(body.Messages))
		for _, m := range body.Messages {
			roles = append(roles, m.Role)
		}
		otherSawHistory.Store(strings.Join(roles, ","))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"carrying on"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer other.Close()

	if err := agent.NewAnthropicProvider("refuse-anth", anth.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider("refuse-anth") })
	if err := agent.NewOpenAIProvider("refuse-other", other.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider("refuse-other") })

	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c := client.New(srv.Addr())
	sess, err := c.CreateSession("refuse-anth/claude-fake", "/p", "")
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = c.Ask(sess.ID, "the request the filter dislikes", 30*time.Second)
	if err == nil {
		t.Fatal("a refused request must fail the turn")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v: a refusal must not go through the retry backoff (2s + 4s)", time.Since(start))
	}
	for _, want := range []string{"model refused the request", "(cyber)", "Cyber Verification Program", "Hint: try another provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "empty response") {
		t.Errorf("a refusal must not read as an empty response: %v", err)
	}
	if n := anthropicCalls.Load(); n != 1 {
		t.Errorf("Anthropic was called %d times, want 1 (no retries)", n)
	}
	msgs, _ := c.GetMessages(sess.ID)
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Errorf("history after the refusal = %+v, want only the user's message", msgs)
	}

	// The hint's promise: the same session, moved to another provider, carries on
	// with its history — including the message the first provider refused.
	if _, err := c.ExecCommand(sess.ID, "model", map[string]any{"model": "refuse-other/m"}); err != nil {
		t.Fatal(err)
	}
	out, err := c.Ask(sess.ID, "continue", 30*time.Second)
	if err != nil || !strings.Contains(out, "carrying on") {
		t.Fatalf("after switching provider: %q %v", out, err)
	}
	// system prompt + the message the first provider refused + the new one
	if got, _ := otherSawHistory.Load().(string); got != "system,user,user" {
		t.Errorf("the new provider saw roles %q, want system,user,user", got)
	}
}
