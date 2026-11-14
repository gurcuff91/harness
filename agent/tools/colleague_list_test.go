package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// listed runs ColleagueList over a directory and decodes what the model receives.
func listed(t *testing.T, dir map[string]Colleague) ([]colleagueListEntry, string) {
	t.Helper()
	out, err := ColleagueList(func() map[string]Colleague { return dir }).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ColleagueList: %v", err)
	}
	var entries []colleagueListEntry
	if json.Unmarshal([]byte(out), &entries) != nil {
		return nil, out // the "no colleagues" sentence
	}
	return entries, out
}

func colleagueAt(url, transport string) Colleague {
	return Colleague{URL: url, Transport: transport, Version: "v9", CWD: "/instance/cwd", PID: os.Getpid() + 1}
}

// What the model receives: per colleague its name, environment, version and ACTIVE
// sessions (id, name, cwd, model, busy) — and no instance-level cwd.
func TestColleagueListShowsActiveSessions(t *testing.T) {
	srv := newFakeColleagueServer()
	srv.addActive("s-1", "Fix the bug", "/proj/a", false)
	srv.addActive("s-2", "Refactor", "/proj/b", true)
	srv.addActive("s-gone", "Closed", "/proj/c", false)
	srv.sessions["s-gone"].closed = true
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	got, raw := listed(t, map[string]Colleague{"raiden": colleagueAt(ts.URL, "tui")})
	if len(got) != 1 || got[0].Name != "raiden" || got[0].Environment != "tui" || got[0].Version != "v9" {
		t.Fatalf("entries = %+v", got)
	}
	if len(got[0].Sessions) != 2 {
		t.Fatalf("sessions = %+v, want the 2 ACTIVE ones (a closed one is not offered)", got[0].Sessions)
	}
	by := map[string]colleagueSession{}
	for _, s := range got[0].Sessions {
		by[s.ID] = s
	}
	if s := by["s-1"]; s.Name != "Fix the bug" || s.CWD != "/proj/a" || s.Model != "fake/model-1" || s.Busy {
		t.Errorf("s-1 = %+v", s)
	}
	if s := by["s-2"]; s.Name != "Refactor" || s.CWD != "/proj/b" || !s.Busy {
		t.Errorf("s-2 = %+v (busy must be reported)", s)
	}
	// the instance's cwd is gone from what the model sees: it lives on the sessions
	var generic []map[string]any
	json.Unmarshal([]byte(raw), &generic)
	if _, has := generic[0]["cwd"]; has {
		t.Errorf("the entry still carries an instance-level cwd: %s", raw)
	}
	if strings.Contains(raw, "/instance/cwd") {
		t.Errorf("the registry's instance cwd leaked into the list: %s", raw)
	}
	if _, has := generic[0]["sessions"]; !has {
		t.Errorf("no sessions field: %s", raw)
	}
}

// A colleague with nothing open is still listed — it can be asked a fresh
// question — and its sessions render as [] (never null).
func TestColleagueListColleagueWithNoSessionsIsStillListed(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	got, raw := listed(t, map[string]Colleague{"empty": colleagueAt(ts.URL, "server")})
	if len(got) != 1 || got[0].Name != "empty" || len(got[0].Sessions) != 0 {
		t.Fatalf("entries = %+v", got)
	}
	if !strings.Contains(raw, `"sessions": []`) {
		t.Errorf("sessions must encode as [] not null: %s", raw)
	}
}

// Liveness: a colleague that does not answer is left out — a dead port and a
// process whose socket accepts but never replies (Ctrl+Z) — and the live one stays.
func TestColleagueListOmitsColleaguesThatDontAnswer(t *testing.T) {
	old := colleagueListTimeout
	colleagueListTimeout = 300 * time.Millisecond
	t.Cleanup(func() { colleagueListTimeout = old })

	live := newFakeColleagueServer()
	live.addActive("s-1", "Alive", "/p", false)
	liveTS := httptest.NewServer(live.handler())
	defer liveTS.Close()

	block := make(chan struct{})
	suspended := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer suspended.Close()
	defer close(block)

	start := time.Now()
	got, _ := listed(t, map[string]Colleague{
		"alive":     colleagueAt(liveTS.URL, "server"),
		"suspended": colleagueAt(suspended.URL, "tui"),
		"dead":      colleagueAt("http://127.0.0.1:1", "slack"),
	})
	if len(got) != 1 || got[0].Name != "alive" {
		t.Fatalf("entries = %+v, want only the colleague that answered", got)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v: the unresponsive ones must cost one bounded round, not hang", elapsed)
	}
}

// The probes run concurrently: N colleagues that each take ~the timeout to fail
// cost about one timeout, not N of them.
func TestColleagueListProbesConcurrently(t *testing.T) {
	old := colleagueListTimeout
	colleagueListTimeout = 400 * time.Millisecond
	t.Cleanup(func() { colleagueListTimeout = old })

	block := make(chan struct{})
	dir := map[string]Colleague{}
	var servers []*httptest.Server
	for i := 0; i < 6; i++ {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
		servers = append(servers, ts)
		dir[fmt.Sprintf("c%d", i)] = colleagueAt(ts.URL, "server")
	}
	defer func() {
		close(block)
		for _, ts := range servers {
			ts.Close()
		}
	}()

	start := time.Now()
	got, out := listed(t, dir)
	elapsed := time.Since(start)
	if len(got) != 0 || !strings.Contains(out, "No other colleagues") {
		t.Fatalf("got %+v / %q", got, out)
	}
	if elapsed > 1500*time.Millisecond { // 6 × 400ms sequentially would be 2.4s
		t.Errorf("6 stuck colleagues took %v: probes are not concurrent", elapsed)
	}
}

// The caller never sees itself, and entries without a URL are skipped.
func TestColleagueListExcludesSelfAndURLless(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	self := colleagueAt(ts.URL, "tui")
	self.PID = os.Getpid()
	got, out := listed(t, map[string]Colleague{
		"me":      self,
		"no-url":  {PID: os.Getpid() + 1, Transport: "tui"},
		"someone": colleagueAt(ts.URL, "server"),
	})
	if len(got) != 1 || got[0].Name != "someone" {
		t.Errorf("entries = %+v / %q", got, out)
	}
}

// Sorted by name, stable output.
func TestColleagueListIsSortedByName(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	got, _ := listed(t, map[string]Colleague{"zeta": colleagueAt(ts.URL, "server"), "alpha": colleagueAt(ts.URL, "server"), "mid": colleagueAt(ts.URL, "server")})
	if len(got) != 3 || got[0].Name != "alpha" || got[1].Name != "mid" || got[2].Name != "zeta" {
		t.Errorf("order = %+v", got)
	}
}

// environment: tui, slack and telegram keep their name; everything else — headless
// serve, the CLI, acp, anything new — is "server". "generic" no longer exists.
func TestEnvironmentLabel(t *testing.T) {
	for transport, want := range map[string]string{
		"tui": "tui", "slack": "slack", "telegram": "telegram",
		"server": "server", "cli": "server", "acp": "server", "discord": "server", "": "server", "generic": "server",
	} {
		if got := environmentLabel(transport); got != want {
			t.Errorf("environmentLabel(%q) = %q, want %q", transport, got, want)
		}
	}
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	got, raw := listed(t, map[string]Colleague{"a": colleagueAt(ts.URL, "cli")})
	if got[0].Environment != "server" || strings.Contains(raw, "generic") {
		t.Errorf("a cli colleague: %+v / %s", got, raw)
	}
}

// Nobody reachable: the same plain sentence as before, not an error.
func TestColleagueListWithNobodyReachable(t *testing.T) {
	_, out := listed(t, map[string]Colleague{})
	if out != "No other colleagues are currently reachable." {
		t.Errorf("out = %q", out)
	}
}

// A colleague that is ALIVE but slow (answers after a delay, well within the
// timeout) must still be listed with its sessions: the list waits for the probes
// to finish before reading their results, instead of returning whatever was ready.
func TestColleagueListWaitsForSlowButLiveColleagues(t *testing.T) {
	old := colleagueListTimeout
	colleagueListTimeout = 2 * time.Second
	t.Cleanup(func() { colleagueListTimeout = old })

	slow := newFakeColleagueServer()
	slow.addActive("s-slow", "Slow one", "/p", false)
	inner := slow.handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("active") == "true" {
			time.Sleep(400 * time.Millisecond) // alive, just slow
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	got, out := listed(t, map[string]Colleague{"slowpoke": colleagueAt(ts.URL, "server")})
	if len(got) != 1 || got[0].Name != "slowpoke" || len(got[0].Sessions) != 1 || got[0].Sessions[0].ID != "s-slow" {
		t.Fatalf("a slow but live colleague was lost or returned empty: %+v\n%s", got, out)
	}
}

// Several live-but-slow colleagues are ALL present (none dropped by a read that
// races its probe) and cost one delay, not the sum of them.
func TestColleagueListManySlowLiveColleaguesAllPresentAndConcurrent(t *testing.T) {
	old := colleagueListTimeout
	colleagueListTimeout = 3 * time.Second
	t.Cleanup(func() { colleagueListTimeout = old })

	dir := map[string]Colleague{}
	for i := 0; i < 5; i++ {
		srv := newFakeColleagueServer()
		srv.addActive(fmt.Sprintf("s-%d", i), fmt.Sprintf("Session %d", i), "/p", false)
		inner := srv.handler()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("active") == "true" {
				time.Sleep(500 * time.Millisecond)
			}
			inner.ServeHTTP(w, r)
		}))
		t.Cleanup(ts.Close)
		dir[fmt.Sprintf("c%d", i)] = colleagueAt(ts.URL, "server")
	}
	start := time.Now()
	got, _ := listed(t, dir)
	elapsed := time.Since(start)
	if len(got) != 5 {
		t.Fatalf("got %d colleagues, want all 5", len(got))
	}
	for _, g := range got {
		if len(g.Sessions) != 1 {
			t.Errorf("%s has %d sessions, want 1", g.Name, len(g.Sessions))
		}
	}
	if elapsed > 1800*time.Millisecond { // sequential would be 5 × 500ms = 2.5s
		t.Errorf("5 colleagues of 500ms each took %v: the probes are not concurrent", elapsed)
	}
}
