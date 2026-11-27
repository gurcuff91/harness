package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// The sub-agent events cross the real SSE framing and the client decodes both.
func TestSubagentEventsReachTheClientDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []types.Event{
			{Type: types.EventSubagentStart, Subagent: "scorpion-hero"},
			{Type: types.EventSubagentStop, Subagent: "scorpion-hero", SubagentResult: types.SubagentOK},
		} {
			w.Write(formatEvent(e))
		}
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	evs, err := client.New(srv.Listener.Addr().String()).StreamEvents(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	var got []client.Event
	for e := range evs {
		got = append(got, e)
	}
	if len(got) != 2 || got[0].Type != "subagent_start" || got[0].Name != "scorpion-hero" || got[0].Result != "" ||
		got[1].Type != "subagent_stop" || got[1].Name != "scorpion-hero" || got[1].Result != "ok" {
		t.Fatalf("decoded = %+v", got)
	}
}

// The detail carries `subagents` and the client's Session and SessionInfo read it.
func TestSessionDetailCarriesSubagentsToTheClient(t *testing.T) {
	d := agent.SessionDetail{Subagents: []string{"a", "b"}}
	b, err := json.Marshal(sessionInfoDTO{Session: d})
	if err != nil {
		t.Fatal(err)
	}
	var info client.SessionInfo
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(info.Session.Subagents) != "[a b]" {
		t.Fatalf("info subagents = %v (%s)", info.Session.Subagents, b)
	}
	b, _ = json.Marshal(d)
	var s client.Session
	json.Unmarshal(b, &s)
	if fmt.Sprint(s.Subagents) != "[a b]" {
		t.Fatalf("session subagents = %v", s.Subagents)
	}
	// none: omitted
	b, _ = json.Marshal(agent.SessionDetail{})
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["subagents"]; ok {
		t.Errorf("subagents must be omitted when none: %s", b)
	}
}

func TestOpenAPISessionDetailDocumentsSubagents(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal([]byte(openAPISpecJSON("127.0.0.1:0")), &spec); err != nil {
		t.Fatal(err)
	}
	if !jsonContainsKey(spec, "subagents") {
		t.Fatal("the OpenAPI spec does not document `subagents`")
	}
}

func jsonContainsKey(v any, key string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if k == key || jsonContainsKey(c, key) {
				return true
			}
		}
	case []any:
		for _, c := range x {
			if jsonContainsKey(c, key) {
				return true
			}
		}
	}
	return false
}
