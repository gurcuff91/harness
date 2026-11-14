package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
)

// boundedClient gives every request a hard deadline: a request that would block on a
// busy session (the very behaviour these tests guard against) must FAIL the test in
// seconds, never hang it until go test's own 10-minute timeout.
var boundedClient = &http.Client{Timeout: 5 * time.Second}

func httpDo(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := boundedClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s did not complete in 5s (a request that should have been refused or answered at once is blocking): %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// GET /api/sessions?active=true lists exactly the sessions OPEN in this server —
// not the persisted ones — with name, cwd, model and busy.
func TestActiveSessionsListsOnlyOpenSessionsWithBusy(t *testing.T) {
	llm := &fakeLLM{gate: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(llm.gate) }) }
	srv, _, model := sharedServer(t, llm)
	t.Cleanup(release) // after the server's own cleanups ⇒ runs BEFORE them (LIFO): open the gate so Close is not stuck on a blocked turn
	c := client.New(srv.Addr())

	idle, err := c.CreateSession(model, "/proj/idle", "Idle one")
	if err != nil {
		t.Fatal(err)
	}
	working, err := c.CreateSession(model, "/proj/work", "Working one")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := c.CreateSession(model, "/proj/closed", "Closed one")
	if err != nil {
		t.Fatal(err)
	}
	c.CloseSession(closed.ID)

	if _, err := c.SendPrompt(working.ID, "start a long turn"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		act, err := c.ListActiveSessions("")
		if err != nil {
			t.Fatal(err)
		}
		busy := false
		for _, a := range act {
			if a.ID == working.ID && a.Busy {
				busy = true
			}
		}
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the working session never showed as busy")
		}
		time.Sleep(10 * time.Millisecond)
	}

	act, err := c.ListActiveSessions("")
	if err != nil {
		t.Fatal(err)
	}
	if len(act) != 2 {
		t.Fatalf("active = %+v, want only the two open sessions (the closed one is persisted, not active)", act)
	}
	by := map[string]client.ActiveSession{}
	for _, a := range act {
		by[a.ID] = a
	}
	if a := by[idle.ID]; a.Name != "Idle one" || a.CWD != "/proj/idle" || a.Model != model || a.Busy {
		t.Errorf("idle = %+v", a)
	}
	if a := by[working.ID]; a.Name != "Working one" || a.CWD != "/proj/work" || !a.Busy {
		t.Errorf("working = %+v (busy must be true while its turn runs)", a)
	}
	if _, ok := by[closed.ID]; ok {
		t.Error("a closed session was listed as active")
	}

	// the cwd filter, and the persisted listing is untouched
	if only, _ := c.ListActiveSessions("/proj/work"); len(only) != 1 || only[0].ID != working.ID {
		t.Errorf("cwd filter = %+v", only)
	}
	if all, err := c.ListSessions(""); err != nil || len(all) != 3 {
		t.Errorf("the persisted listing must still hold all 3 sessions: %d %v", len(all), err)
	}
	release()
}

// An empty server lists [] (never null).
func TestActiveSessionsEmptyIsAnArray(t *testing.T) {
	srv, _, _ := sharedServer(t, nil)
	code, body := httpDo(t, "GET", "http://"+srv.Addr()+"/api/sessions?active=true", "")
	if code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("%d %q", code, body)
	}
}

// The listing is a lock-free read: it answers promptly while a turn is running.
func TestActiveSessionsDoesNotBlockOnARunningTurn(t *testing.T) {
	llm := &fakeLLM{gate: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(llm.gate) }) }
	srv, _, model := sharedServer(t, llm)
	t.Cleanup(release) // after the server's own cleanups ⇒ runs BEFORE them (LIFO): open the gate so Close is not stuck on a blocked turn
	c := client.New(srv.Addr())
	s, _ := c.CreateSession(model, "/p", "")
	c.SendPrompt(s.ID, "long")
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if _, err := c.ListActiveSessions(""); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("listing took %v while a turn was in flight", time.Since(start))
	}
	release()
}

// no_queue on /ask: a busy session answers 409 "session is busy" at once, nothing
// reaches the model and nothing is written to the conversation.
func TestAskNoQueueOnABusySessionIs409AndStoresNothing(t *testing.T) {
	llm := &fakeLLM{gate: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(llm.gate) }) }
	srv, _, model := sharedServer(t, llm)
	t.Cleanup(release) // after the server's own cleanups ⇒ runs BEFORE them (LIFO): open the gate so Close is not stuck on a blocked turn
	c := client.New(srv.Addr())
	s, _ := c.CreateSession(model, "/p", "")
	c.SendPrompt(s.ID, "the user is working")
	deadline := time.Now().Add(5 * time.Second)
	for {
		act, _ := c.ListActiveSessions("")
		if len(act) == 1 && act[0].Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never busy")
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	code, body := httpDo(t, "POST", "http://"+srv.Addr()+"/api/sessions/"+s.ID+"/ask", `{"text":"colleague question","origin":"colleague","no_queue":true}`)
	if code != http.StatusConflict || !strings.Contains(body, "session is busy") {
		t.Fatalf("%d %s, want 409 session is busy", code, body)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the refusal took %v: it must not wait for the turn", time.Since(start))
	}
	// /prompt follows the same rule
	if code, body := httpDo(t, "POST", "http://"+srv.Addr()+"/api/sessions/"+s.ID+"/prompt", `{"text":"x","no_queue":true}`); code != http.StatusConflict {
		t.Errorf("/prompt no_queue while busy = %d %s", code, body)
	}

	release()
	deadline = time.Now().Add(5 * time.Second)
	for {
		act, _ := c.ListActiveSessions("")
		if len(act) == 1 && !act[0].Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never idle again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	msgs, _ := c.GetMessages(s.ID)
	for _, m := range msgs {
		if len(m.Parts) > 0 && strings.Contains(m.Parts[0].Text, "colleague question") {
			t.Fatalf("a refused prompt was stored in the conversation: %+v", msgs)
		}
	}
	llm.mu.Lock()
	n := len(llm.seen)
	llm.mu.Unlock()
	if n != 1 {
		t.Errorf("the model saw %d requests, want 1 (the user's only)", n)
	}
}

// Without no_queue a busy session still queues (the default is untouched), and an
// idle session with no_queue just answers.
func TestAskQueuesByDefaultAndNoQueueOnIdleJustAnswers(t *testing.T) {
	srv, _, model := sharedServer(t, nil)
	c := client.New(srv.Addr())
	s, _ := c.CreateSession(model, "/p", "")
	out, err := c.Ask(s.ID, "hello", 5*time.Second, client.WithoutQueue(), client.WithOrigin("colleague"))
	if err != nil || out != "ok" {
		t.Fatalf("idle + no_queue: %q %v", out, err)
	}
	out, err = c.Ask(s.ID, "again", 5*time.Second)
	if err != nil || out != "ok" {
		t.Fatalf("default ask: %q %v", out, err)
	}
}

// The origin is persisted with the message, so a reloaded history keeps it.
func TestColleagueOriginIsStoredWithTheMessage(t *testing.T) {
	srv, _, model := sharedServer(t, nil)
	c := client.New(srv.Addr())
	s, _ := c.CreateSession(model, "/p", "")
	if _, err := c.Ask(s.ID, "from a colleague", 10*time.Second, client.WithOrigin("colleague")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ask(s.ID, "from the user", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	msgs, _ := c.GetMessages(s.ID)
	var got []string
	for _, m := range msgs {
		if m.Role == "user" && m.Meta != nil {
			got = append(got, m.Meta.Origin)
		}
	}
	if len(got) != 1 || got[0] != "colleague" {
		t.Errorf("origins = %v: only the colleague's prompt carries one", got)
	}
	// and the model is never told: the text is delivered as sent
	llm := msgs[0].Parts[0].Text
	if llm != "from a colleague" {
		t.Errorf("message text = %q: the origin must not alter what the model reads", llm)
	}
}

// The client turns the 409 into something a caller can test for.
func TestClientIsBusyRecognisesTheRefusal(t *testing.T) {
	llm := &fakeLLM{gate: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(llm.gate) }) }
	srv, _, model := sharedServer(t, llm)
	t.Cleanup(release) // after the server's own cleanups ⇒ runs BEFORE them (LIFO): open the gate so Close is not stuck on a blocked turn
	c := client.New(srv.Addr())
	s, _ := c.CreateSession(model, "/p", "")
	c.SendPrompt(s.ID, "busy")
	time.Sleep(300 * time.Millisecond)
	_, err := c.Ask(s.ID, "x", 3*time.Second, client.WithoutQueue())
	if !client.IsBusy(err) {
		t.Fatalf("err = %v, want a busy refusal", err)
	}
	var ae *client.Error
	if !errors.As(err, &ae) || ae.Status != 409 {
		t.Errorf("Status = %+v, want 409", ae)
	}
	// a different 409-less failure is not "busy"
	if client.IsBusy(errors.New("session is busy")) {
		t.Error("a plain error with the same text is not a busy refusal (it carries no 409)")
	}
	release()
}

// The OpenAPI document describes the new parameter, status and schema.
func TestOpenAPIDocumentsColleagueSessionFeatures(t *testing.T) {
	var spec struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	raw := openAPISpecJSON("127.0.0.1:0")
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Components.Schemas["PromptRequest"].Properties["no_queue"]; !ok {
		t.Error("PromptRequest does not document no_queue")
	}
	var detail struct {
		AllOf []struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"allOf"`
	}
	var rawSpec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(raw), &rawSpec); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawSpec.Components.Schemas["SessionDetail"], &detail); err != nil {
		t.Fatal(err)
	}
	documented := false
	for _, part := range detail.AllOf {
		if _, ok := part.Properties["resumed"]; ok {
			documented = true
		}
	}
	if !documented {
		t.Error("SessionDetail does not document resumed")
	}
	for _, f := range []string{"id", "name", "cwd", "model", "busy"} {
		if _, ok := spec.Components.Schemas["ActiveSession"].Properties[f]; !ok {
			t.Errorf("ActiveSession does not document %q", f)
		}
	}
	for _, p := range []string{"/api/sessions/{id}/ask", "/api/sessions/{id}/prompt"} {
		var op struct {
			Responses map[string]json.RawMessage `json:"responses"`
		}
		json.Unmarshal(spec.Paths[p]["post"], &op)
		if _, ok := op.Responses["409"]; !ok {
			t.Errorf("%s does not document the 409", p)
		}
	}
}
