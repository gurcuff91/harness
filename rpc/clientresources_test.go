package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
)

// the JSON of resources/load as a client writes it (the tags are part of the
// protocol); a skill without location is valid.
func TestResourcesWireShape(t *testing.T) {
	var r resources.Resources
	in := `{"system_md":"sys","agents_md":"ctx","skills":[{"name":"a","description":"d","content":"c"},{"name":"b","description":"d","location":"/x/SKILL.md","content":"c"}]}`
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.SystemMD != "sys" || r.AgentsMD != "ctx" || len(r.Skills) != 2 || r.Skills[0].Location != "" || r.Skills[1].Location != "/x/SKILL.md" || r.Skills[0].Content != "c" {
		t.Fatalf("decoded = %+v (are the snake_case tags in agent/resources?)", r)
	}
}

func TestClientResourcesLoadOnNewResumeFork(t *testing.T) {
	model := registerScript(t, &scriptModel{script: map[string][]scriptCall{
		"use deploy": {{"Skill", `{"name":"deploy"}`}},
		"use remote": {{"Skill", `{"name":"remote"}`}},
	}})
	res := &clientResImpl{def: resources.Resources{
		AgentsMD: "project rules",
		Skills: []resources.Skill{
			{Name: "deploy", Description: "ship it", Location: "/skills/deploy/SKILL.md", Content: "run the deploy script"},
			{Name: "remote", Description: "from a database", Content: "no path here"},
		},
	}}
	c := start(t)
	c.serveMemRes(nil, res)
	c.initialize(clientMemResCfg())

	cwd := t.TempDir()
	var d agent.SessionDetail
	c.must("session/new", SessionNewParams{CWD: cwd, Model: model}, &d)
	sp := c.sysprompt(d.ID)
	for _, want := range []string{"- deploy: ship it", "- remote: from a database", "project rules"} {
		if !strings.Contains(sp, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
	got := c.ask(d.ID, "use deploy")
	if !strings.Contains(got, "run the deploy script") || !strings.Contains(got, "/skills/deploy") {
		t.Fatalf("Skill result = %q", got)
	}
	got = c.ask(d.ID, "use remote")
	if !strings.Contains(got, "no path here") {
		t.Fatalf("Skill result for a skill without location = %q", got)
	}

	c.must("session/close", SessionIDParams{ID: d.ID}, nil)
	var rd agent.SessionDetail
	c.must("session/resume", SessionResumeParams{ID: d.ID}, &rd)
	var fd agent.SessionDetail
	c.must("session/fork", SessionForkParams{ID: d.ID}, &fd)
	got3 := res.received()
	if len(got3) < 3 {
		t.Fatalf("resources/load calls = %v, want one per new, resume and fork", got3)
	}
	for i, cw := range got3[:3] {
		if cw != cwd {
			t.Fatalf("call %d carried cwd %q, want %q", i, cw, cwd)
		}
	}
	if !strings.Contains(c.sysprompt(fd.ID), "- deploy: ship it") {
		t.Fatal("a fork lacks the client's skills")
	}
}

func TestClientResourcesFailureOnNewVersusResume(t *testing.T) {
	for _, mode := range []string{"error", "hang"} {
		t.Run(mode, func(t *testing.T) {
			shortTimeout(t, 150*time.Millisecond)
			model := registerScript(t, &scriptModel{})
			res := &clientResImpl{def: resources.Resources{AgentsMD: "rules", Skills: []resources.Skill{{Name: "s", Description: "d", Content: "c"}}}}
			c := start(t)
			c.serveMemRes(nil, res)
			c.initialize(clientMemResCfg())
			var d agent.SessionDetail
			c.must("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, &d)
			c.must("session/close", SessionIDParams{ID: d.ID}, nil)

			res.mu.Lock()
			want := "disk is gone"
			if mode == "error" {
				res.fail = want
			} else {
				res.hang = true
				want = "the client did not answer resources/load within 150ms"
			}
			res.mu.Unlock()

			// The client's loader failing is an upstream failure, not an internal fault.
			je := c.fail("session/new", SessionNewParams{CWD: t.TempDir(), Model: model}, CodeUpstream, "upstream")
			if !strings.Contains(je.Message, want) {
				t.Fatalf("session/new message = %q, want it to contain %q", je.Message, want)
			}
			var rd agent.SessionDetail
			c.must("session/resume", SessionResumeParams{ID: d.ID}, &rd) // still opens, without context
			sp := c.sysprompt(rd.ID)
			if strings.Contains(sp, "rules") || strings.Contains(sp, "Available Skills") {
				t.Fatal("a resumed session whose load failed must have no context")
			}
		})
	}
}

// ten sessions opening at once: ten requests in flight together, each answered
// with its own cwd's resources.
func TestClientResourcesConcurrentSessionOpens(t *testing.T) {
	const n = 10
	model := registerScript(t, &scriptModel{})
	res := &clientResImpl{by: map[string]resources.Resources{}}
	cwds := make([]string, n)
	for i := range cwds {
		cwds[i] = fmt.Sprintf("/work/p%d", i)
		res.by[cwds[i]] = resources.Resources{Skills: []resources.Skill{{Name: fmt.Sprint("skill", i), Description: "d", Content: "c"}}}
	}
	var mu sync.Mutex
	inflight, peak := 0, 0
	release := make(chan struct{})
	var once sync.Once
	res.gate = func(string) {
		mu.Lock()
		inflight++
		if inflight > peak {
			peak = inflight
		}
		if inflight == n {
			once.Do(func() { close(release) })
		}
		mu.Unlock()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}
	c := start(t)
	c.serveMemRes(nil, res)
	c.initialize(clientMemResCfg())

	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range cwds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			raw, err := c.conn.Call(ctx, "session/new", SessionNewParams{CWD: cwds[i], Model: model})
			if err != nil {
				t.Errorf("session %d: %v", i, err)
				return
			}
			var d agent.SessionDetail
			json.Unmarshal(raw, &d)
			ids[i] = d.ID
		}(i)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if peak != n {
		t.Fatalf("peak concurrent resources/load = %d, want %d", peak, n)
	}
	for i, id := range ids {
		sp := c.sysprompt(id)
		if !strings.Contains(sp, fmt.Sprintf("- skill%d: d", i)) {
			t.Fatalf("session %d got another cwd's skills", i)
		}
	}
}

// every request the binary sends has a doc and wire types, is unique, and is in
// the schema; the wire fields are snake_case.
func TestReverseCallsAreDocumented(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range clientCalls {
		if seen[c.Method] {
			t.Errorf("%s is declared twice", c.Method)
		}
		seen[c.Method] = true
		if c.Doc == "" || c.Params == nil || c.Result == nil {
			t.Errorf("%s: incomplete (doc/params/result)", c.Method)
		}
		for _, typ := range []reflect.Type{c.Params, c.Result} {
			for i := 0; typ.Kind() == reflect.Struct && i < typ.NumField(); i++ {
				tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
				if typ.Field(i).IsExported() && (tag == "" || tag != strings.ToLower(tag)) {
					t.Errorf("%s: field %s.%s has json tag %q, want snake_case", c.Method, typ.Name(), typ.Field(i).Name, tag)
				}
			}
		}
	}
	for _, m := range []string{"memory/write", "memory/get", "memory/search", "memory/delete", "resources/load", "settings/swap", "credentials/get", "tool/call"} {
		if !seen[m] {
			t.Errorf("%s is not a documented reverse call", m)
		}
	}
	doc, _ := Schema()
	for m := range seen {
		if !strings.Contains(string(doc), `"method": "`+m+`"`) && !strings.Contains(string(doc), `"method":"`+m+`"`) {
			t.Errorf("%s is missing from Schema()", m)
		}
	}
}
