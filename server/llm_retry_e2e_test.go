package server

import (
	"context"
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
	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/logx"
)

// End to end through a REAL provider client (CustomOpenAI on the provider
// transport), the session, the server and SSE: the first completion request
// gets headers and then total silence (a dead connection mid-response, before
// any content); the idle timeout aborts it, the turn retries on a new request,
// the second attempt answers, and the client sees a "retry" event and a normal
// reply — with no broken assistant message in the history.
func TestDeadStreamIsRetriedEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	llm.SetStreamIdleTimeoutForTest(t, 300*time.Millisecond)

	var calls atomic.Int64
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 { // first attempt: headers, then silence
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"recovered"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer gw.Close()
	if err := agent.NewOpenAIProvider("e2e-retry-gw", gw.URL); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c := client.New(srv.Addr())
	sess, err := c.CreateSession("e2e-retry-gw/m", "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev, _ := c.StreamEvents(ctx, sess.ID)
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	c.SendPrompt(sess.ID, "hi")
	var retry *client.Event
	var text string
	for e := range ev {
		switch e.Type {
		case "retry":
			e := e
			retry = &e
		case "text":
			text += e.Delta
		case "error":
			t.Fatalf("turn errored: %s", e.Message)
		}
		if e.Type == "turn_end" {
			break
		}
	}
	if retry == nil || retry.Attempt != 2 || retry.Max != 3 || !strings.Contains(retry.Error, "idle") {
		t.Fatalf("retry event = %+v, want attempt 2/3 for the idle stream", retry)
	}
	if text != "recovered" || calls.Load() != 2 {
		t.Fatalf("text = %q after %d requests, want the second attempt's reply", text, calls.Load())
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("recovery took %v", el)
	}
	msgs, _ := c.GetMessages(sess.ID)
	var assistant []string
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, p := range m.Parts {
				assistant = append(assistant, p.Text)
			}
		}
	}
	if len(assistant) != 1 || assistant[0] != "recovered" {
		t.Fatalf("history assistant parts = %q, want exactly [recovered]", assistant)
	}
}
