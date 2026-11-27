package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// toolCallingLLM serves an OpenAI chat-completions stream that calls the tool
// `name` once (when the last message is not a tool result) and answers "done"
// after it.
func toolCallingLLM(t *testing.T, name string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(body.Messages) > 0 && body.Messages[len(body.Messages)-1].Role == "tool" {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"}}]}`+"\n\n")
		} else {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":%q,"arguments":"{}"}}]}}]}`+"\n\n", name)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	withCleanProviderRegistry(t)
	prov := "idctx-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if err := admin.RegisterOpenAI(prov, srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider(prov) })
	return prov + "/m"
}

// A tool sees the session that called it: two sessions running at the same
// time each see their own id.
func TestToolCtxCarriesItsOwnSessionID(t *testing.T) {
	model := toolCallingLLM(t, "WhoAmI")
	var mu sync.Mutex
	seen := map[string]string{} // session id the tool saw → itself
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	tool := tools.Tool{
		Def: types.ToolDef{Name: "WhoAmI", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			id, ok := SessionID(ctx)
			if !ok {
				return "", fmt.Errorf("no session in ctx")
			}
			started <- struct{}{}
			<-release // both sessions are inside the tool together
			mu.Lock()
			seen[id] = id
			mu.Unlock()
			return id, nil
		},
	}
	a := New(AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.Nil(), Tools: []tools.Tool{tool}})
	t.Cleanup(func() { a.Close() })

	var sessions []*Session
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		s, err := a.NewSession(t.TempDir(), model)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		sessions = append(sessions, s)
		go func() {
			_, err := s.PromptAndWait(context.Background(), "go")
			errs <- err
		}()
	}
	<-started
	<-started
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range sessions {
		if seen[s.ID()] != s.ID() {
			t.Errorf("session %s: the tool saw %v", s.ID(), seen)
		}
	}
	if len(seen) != 2 {
		t.Errorf("seen = %v, want two distinct ids", seen)
	}
}

func TestSessionIDOfAPlainContext(t *testing.T) {
	if id, ok := SessionID(context.Background()); ok || id != "" {
		t.Fatalf("got %q, %v", id, ok)
	}
}
