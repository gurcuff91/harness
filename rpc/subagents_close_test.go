package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
)

// gatedSubagentModel scripts a parent that launches one sub-agent, and holds
// the sub-agent's own request open until released, so the sub-agent is still
// running when the session is closed.
type gatedSubagentModel struct {
	hold    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (g *gatedSubagentModel) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
		return
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "text/event-stream")
	// The PARENT is the request that offers the Subagent tool: a sub-agent never
	// has it (no recursion). That is how the two are told apart.
	parent := false
	for _, tl := range body.Tools {
		parent = parent || tl.Function.Name == "Subagent"
	}
	last := ""
	if n := len(body.Messages); n > 0 {
		last = fmt.Sprint(body.Messages[n-1].Content)
		if body.Messages[n-1].Role == "tool" {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"launched"}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\ndata: [DONE]\n\n")
			return
		}
	}
	switch {
	case parent && strings.Contains(last, "LAUNCH"):
		// "arguments" is a JSON STRING holding the JSON arguments: encode it twice.
		args, _ := json.Marshal(`{"prompt":"SUBTASK slow work"}`)
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"Subagent","arguments":%s}}]}}]}`+"\n\n", args)
	case !parent && strings.Contains(last, "SUBTASK"):
		g.once.Do(func() { close(g.started) })
		select { // the sub-agent's own model call: held until the test ends or the request is cancelled
		case <-g.hold:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done late"}}]}`+"\n\n")
	default:
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
	}
	fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\ndata: [DONE]\n\n")
}

// Closing a session over RPC cancels its live sub-agents and the client SEES
// each one end (subagent_stop, result "cancelled"): the session is closed
// before the event watcher, so the stop events are not dropped and the client
// is left with no ghost in its list. Nothing is delivered as a reply.
func TestClosingASessionOverRPCDeliversTheCancelledStops(t *testing.T) {
	g := &gatedSubagentModel{hold: make(chan struct{}), started: make(chan struct{})}
	ts := httptest.NewServer(http.HandlerFunc(g.handler))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(g.hold) })
	prov := "rpc-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	if err := admin.RegisterOpenAI(prov, ts.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.UnregisterProvider(prov) })

	c := start(t)
	c.initialize(memStoreConfig())
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: prov + "/m"}, &d)
	c.must("session/prompt", SessionPromptParams{ID: d.ID, Text: "LAUNCH it"}, nil)

	var name string
	deadline := time.After(10 * time.Second)
	for name == "" {
		select {
		case e := <-c.events:
			if e["type"] == "subagent_start" {
				name = fmt.Sprint(e["name"])
			}
		case <-deadline:
			t.Fatal("the sub-agent never started")
		}
	}
	select {
	case <-g.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the sub-agent never reached its model")
	}

	// state, not only events: the live name is in the session detail.
	var info SessionInfoResult
	c.must("session/info", SessionIDParams{ID: d.ID}, &info)
	if len(info.Session.Subagents) != 1 || info.Session.Subagents[0] != name {
		t.Fatalf("session/info subagents = %v, want [%s]", info.Session.Subagents, name)
	}

	c.must("session/close", SessionIDParams{ID: d.ID}, nil)

	var stop map[string]any
	deadline = time.After(10 * time.Second)
	for stop == nil {
		select {
		case e := <-c.events:
			if e["type"] == "subagent_stop" {
				stop = e
			}
			if e["type"] == "received_prompt" && strings.HasPrefix(fmt.Sprint(e["origin"]), "subagent:") {
				t.Fatalf("a cancelled sub-agent delivered a reply: %v", e)
			}
		case <-deadline:
			t.Fatal("closing the session never delivered subagent_stop to the client: a ghost would stay in its list")
		}
	}
	if stop["name"] != name || stop["result"] != "cancelled" {
		t.Fatalf("stop = %v, want name %s result cancelled", stop, name)
	}
}
