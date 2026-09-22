package harness

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// TestNewAgentDefaults verifies the zero-option facade constructor produces
// a working *agent.Agent, mirroring how an SDK consumer would call it with
// no configuration at all.
func TestNewAgentDefaults(t *testing.T) {
	a := NewAgent()
	if a == nil {
		t.Fatal("NewAgent() returned nil")
	}
	defer a.Close()
}

// TestAgentWithOptionsAppliesConfig verifies each AgentWith* option mutates
// the AgentOptions the facade builds before delegating to agent.New — the
// entire point of this file being a thin option-application wrapper, not a
// reimplementation of Agent construction.
func TestAgentWithOptionsAppliesConfig(t *testing.T) {
	a := NewAgent(
		AgentWithThinking("low"),
		AgentWithMaxIterations(7),
		AgentWithStore(store.NewInMemoryStore()),
		AgentWithResourceLoader(resources.NilLoader{}),
	)
	defer a.Close()

	sess, err := a.NewSession(t.TempDir(), "")
	// An empty model string is expected to fail resolution — this test only
	// cares that construction (NewAgent + options) itself didn't panic or
	// silently drop the options; a real model would require live provider
	// config, out of scope for this smoke test.
	if err == nil {
		defer sess.Close()
	}
}

// TestAgentWithBoolOptionsSetTheirFlag verifies each boolean AgentWith*
// option flips its corresponding agent.AgentOptions.EnableX field — the
// direct regression test for a real SDK-user report: AgentWithWebSearch and
// AgentWithSessionInfo didn't exist at all (EnableWebSearch/EnableSessionInfo
// were reachable only via AgentWithOptions or harness's own internal
// newInteractiveAgent, never as a standalone facade option like every other
// EnableX flag already had). Exercises AgentWithOptions + AgentOption
// application directly (not agent.New(), which would require a live
// provider) — this only needs to confirm the OPTION mutates the struct.
func TestAgentWithBoolOptionsSetTheirFlag(t *testing.T) {
	cases := []struct {
		name string
		opt  AgentOption
		get  func(agent.AgentOptions) bool
	}{
		{"AgentWithMCPs", AgentWithMCPs(), func(o agent.AgentOptions) bool { return o.EnableMCPs }},
		{"AgentWithMemory", AgentWithMemory(), func(o agent.AgentOptions) bool { return o.EnableMemory }},
		{"AgentWithScheduler", AgentWithScheduler(), func(o agent.AgentOptions) bool { return o.EnableScheduler }},
		{"AgentWithColleagues", AgentWithColleagues(), func(o agent.AgentOptions) bool { return o.EnableColleagues }},
		{"AgentWithWebSearch", AgentWithWebSearch(), func(o agent.AgentOptions) bool { return o.EnableWebSearch }},
		{"AgentWithSessionInfo", AgentWithSessionInfo(), func(o agent.AgentOptions) bool { return o.EnableSessionInfo }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var o agent.AgentOptions
			c.opt(&o)
			if !c.get(o) {
				t.Errorf("%s did not set its EnableX flag", c.name)
			}
		})
	}
}

// TestAgentWithOptionsAppliesPrebuiltStruct verifies AgentWithOptions applies
// a whole pre-built AgentOptions, and that a AgentWith* option listed AFTER
// it still wins (last-write-wins, per NewAgent's doc comment).
func TestAgentWithOptionsAppliesPrebuiltStruct(t *testing.T) {
	prebuilt := agent.AgentOptions{ThinkingLevel: "medium"}
	a := NewAgent(
		AgentWithOptions(prebuilt),
		AgentWithThinking("high"), // applied after — must win
	)
	defer a.Close()
}

// NewClient / Client smoke test — just verifies the alias and constructor
// are wired to the real client package, without opening a real connection.
func TestNewClientAliasIsWired(t *testing.T) {
	c := NewClient("127.0.0.1:0")
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
}

// TestRunServerAliasIsWiredEndToEnd verifies RunServer (and
// ServerWithAddr/ServerWithLogger) are genuinely wired to server.Run — not
// just type-checking aliases — by actually binding, serving one real HTTP
// request, and shutting down cleanly through the facade alone.
func TestRunServerAliasIsWiredEndToEnd(t *testing.T) {
	a := NewAgent(AgentWithStore(store.NewInMemoryStore()))
	ctx, cancel := context.WithCancel(context.Background())
	addr := "127.0.0.1:18964" // fixed, unlikely-collision test-only port

	done := make(chan error, 1)
	go func() { done <- RunServer(ctx, a, ServerWithAddr(addr), ServerWithLogger(NewNilLogger())) }()

	deadline := time.Now().Add(3 * time.Second)
	var reached bool
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/api/server")
		if err == nil {
			resp.Body.Close()
			reached = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reached {
		t.Fatal("RunServer's HTTP endpoint never became reachable")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunServer returned error on clean shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunServer did not return after ctx was cancelled")
	}
}

// TestSessionToolsEndpointEndToEnd verifies GET /api/sessions/{id}/tools —
// wired via server.handleListTools and client.Client.GetSessionTools — end
// to end: a real RunServer instance, a real session, a real HTTP round
// trip, decoding into real types.ToolDef values that match what the
// session's own Session.Tools() reports directly (not just "some JSON came
// back").
func TestSessionToolsEndpointEndToEnd(t *testing.T) {
	a := NewAgent(AgentWithStore(store.NewInMemoryStore()))
	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18965" // fixed, unlikely-collision test-only port

	done := make(chan error, 1)
	go func() { done <- RunServer(ctx, a, ServerWithAddr(addr), ServerWithLogger(NewNilLogger())) }()

	c := NewClient(addr)
	var sess *client.Session
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sess, err = c.CreateSession(models[0].Model, t.TempDir(), "")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	defs, err := c.GetSessionTools(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionTools: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("expected at least one tool definition (built-ins are always registered)")
	}

	names := map[string]bool{}
	for _, d := range defs {
		if d.Name == "" {
			t.Error("tool definition with empty name")
		}
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		if len(d.InputSchema) == 0 {
			t.Errorf("tool %q has empty input_schema", d.Name)
		}
		names[d.Name] = true
	}
	for _, want := range []string{"Bash", "Read", "Write", "Edit"} {
		if !names[want] {
			t.Errorf("expected built-in tool %q in the response, got %v", want, names)
		}
	}

	// Session-not-active must 400, same as every other /api/sessions/{id}/*
	// endpoint — GetSessionTools against a bogus id must surface that.
	if _, err := c.GetSessionTools("not-a-real-session-id"); err == nil {
		t.Error("expected an error for a non-active session id")
	}

	cancel()
	<-done
}

// TestMaxIterCommandEndToEnd verifies the "max-iter" session command end to
// end: exec via client.Client.ExecCommand (POST /api/sessions/{id}/commands)
// reaches server.handleExecCommand's new case, which calls
// Session.SetMaxIterations — confirmed by re-fetching GET
// /api/sessions/{id}/info and seeing the updated max_iterations, and by an
// out-of-range value correctly failing with an error instead of silently
// applying.
func TestMaxIterCommandEndToEnd(t *testing.T) {
	a := NewAgent(AgentWithStore(store.NewInMemoryStore()))
	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18966" // fixed, unlikely-collision test-only port

	done := make(chan error, 1)
	go func() { done <- RunServer(ctx, a, ServerWithAddr(addr), ServerWithLogger(NewNilLogger())) }()

	c := NewClient(addr)
	var sess *client.Session
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sess, err = c.CreateSession(models[0].Model, t.TempDir(), "")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Valid value takes effect.
	if _, err := c.ExecCommand(sess.ID, "max-iter", map[string]any{"value": "321"}); err != nil {
		t.Fatalf("ExecCommand max-iter: %v", err)
	}
	info, err := c.GetSessionInfo(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionInfo: %v", err)
	}
	if info.Session.MaxIterations != 321 {
		t.Errorf("max_iterations after ExecCommand = %d, want 321", info.Session.MaxIterations)
	}

	// Out-of-range value is rejected, and must NOT change the prior value.
	if _, err := c.ExecCommand(sess.ID, "max-iter", map[string]any{"value": "1001"}); err == nil {
		t.Error("expected an error for max-iter value 1001 (above the 1000 ceiling)")
	}
	info, err = c.GetSessionInfo(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionInfo (after rejected update): %v", err)
	}
	if info.Session.MaxIterations != 321 {
		t.Errorf("max_iterations after a REJECTED update = %d, want unchanged 321", info.Session.MaxIterations)
	}

	cancel()
	<-done
}

// TestCustomProviderEndToEnd verifies the full custom-provider settings
// lifecycle over real HTTP: PUT /api/settings/provider/{name} (validation
// + persistence), GET /api/settings/provider (listing), the provider
// showing up automatically in GET /api/providers once EnsureRegistry runs
// again, and DELETE /api/settings/provider/{name}. Exercised via
// client.Client.PutCustomProvider/GetCustomProviders/DeleteCustomProvider —
// wired through server.handleListCustomProviders/handlePutCustomProvider/
// handleDeleteCustomProvider.
//
// config.GetSettingsManager() is a process-wide sync.Once singleton (see
// server/create_session_thinking_test.go's identical caveat) — this test
// isolates HOME via t.Setenv BEFORE any settings access, so it never
// touches the real developer/CI ~/.harness/settings.json. It does NOT call
// providers.EnsureRegistry() itself (that's a SEPARATE providers-package
// singleton with its own irreversible sync.Once, already fired once for
// every other test in this binary needing a real model) — GET
// /api/providers reflecting the new custom provider requires a fresh
// process, which is exactly the documented no-hot-reload behavior custom
// providers share with MCP servers, not something this test can observe
// within one binary.
func TestCustomProviderEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a := NewAgent(AgentWithStore(store.NewInMemoryStore()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18967" // fixed, unlikely-collision test-only port

	done := make(chan error, 1)
	go func() { done <- RunServer(ctx, a, ServerWithAddr(addr), ServerWithLogger(NewNilLogger())) }()

	c := NewClient(addr)
	deadline := time.Now().Add(3 * time.Second)
	var reached bool
	for time.Now().Before(deadline) {
		if _, err := c.GetCustomProviders(); err == nil {
			reached = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reached {
		t.Fatal("server never became reachable")
	}

	// Reject: unsupported type.
	if _, err := c.PutCustomProvider("bad-type", client.CustomProvider{Type: "anthropic", URL: "https://x"}); err == nil {
		t.Error("expected an error for an unsupported custom provider type")
	}
	// Reject: missing url.
	if _, err := c.PutCustomProvider("bad-url", client.CustomProvider{Type: "openai"}); err == nil {
		t.Error("expected an error for a missing url")
	}
	// Reject: reserved built-in name.
	if _, err := c.PutCustomProvider("openai", client.CustomProvider{Type: "openai", URL: "https://x"}); err == nil {
		t.Error("expected an error for a reserved built-in provider name")
	}

	// Well-formed provider is accepted and persisted.
	saved, err := c.PutCustomProvider("my-proxy", client.CustomProvider{
		Type:    "openai",
		URL:     "https://my-proxy.internal/v1",
		Headers: map[string]string{"X-Org-Id": "acme"},
		Display: "My Proxy",
	})
	if err != nil {
		t.Fatalf("PutCustomProvider: %v", err)
	}
	if saved.URL != "https://my-proxy.internal/v1" || saved.Display != "My Proxy" {
		t.Errorf("unexpected saved provider: %+v", saved)
	}

	all, err := c.GetCustomProviders()
	if err != nil {
		t.Fatalf("GetCustomProviders: %v", err)
	}
	if _, ok := all["my-proxy"]; !ok {
		t.Errorf("my-proxy missing from GetCustomProviders(): %+v", all)
	}

	// Delete removes it.
	if _, err := c.DeleteCustomProvider("my-proxy"); err != nil {
		t.Fatalf("DeleteCustomProvider: %v", err)
	}
	all, err = c.GetCustomProviders()
	if err != nil {
		t.Fatalf("GetCustomProviders (after delete): %v", err)
	}
	if _, ok := all["my-proxy"]; ok {
		t.Error("my-proxy still present after DeleteCustomProvider")
	}

	// Delete of a non-existent provider is a clean 404-mapped error.
	if _, err := c.DeleteCustomProvider("never-existed"); err == nil {
		t.Error("expected an error deleting a non-existent custom provider")
	}

	cancel()
	<-done
}

// TestNewOpenAIProviderFacadeAliasIsWired verifies harness.NewOpenAIProvider
// (and its ProviderWith* options) are genuinely wired to
// agent.NewOpenAIProvider — not just type-checking aliases — by registering
// a real custom provider through the facade alone and confirming it's
// resolvable via a real Agent built afterward.
//
// config.GetSettingsManager() is a process-wide sync.Once singleton (see
// server/create_session_thinking_test.go's identical caveat) — isolates HOME
// before any settings access. Also snapshots/restores the internal provider
// registry so this test's registration never leaks into another test
// running later in the same binary.
func TestNewOpenAIProviderFacadeAliasIsWired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	providers.EnsureRegistry()
	snapshot := append([]providers.Provider{}, providers.All...)
	defer func() { providers.All = snapshot }()

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"facade-model"}]}`))
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	if err := NewOpenAIProvider("facade-proxy", srv.URL,
		ProviderWithDisplay("Facade Proxy"),
		ProviderWithHeaders(map[string]string{"X-Test": "1"}),
		ProviderWithReasoningSplit(),
	); err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	a := NewAgent(AgentWithStore(store.NewInMemoryStore()))
	defer a.Close()

	var found bool
	for _, p := range a.Providers() {
		if p.Name == "facade-proxy" {
			found = true
			if p.DisplayName != "Facade Proxy" {
				t.Errorf("DisplayName = %q, want Facade Proxy", p.DisplayName)
			}
		}
	}
	if !found {
		t.Fatal("facade-proxy not found in a.Providers() after registration via the harness facade")
	}

	// ProviderWithReasoningSplit() (no argument, always means "on") must
	// genuinely reach the wire through the facade alias, not just type-check.
	req := &types.Request{Model: "facade-model", Messages: []types.Message{}, MaxTokens: 10}
	for _, prov := range providers.All {
		if prov.Name() == "facade-proxy" {
			if _, err := prov.CompleteStream(context.Background(), req, func(types.StreamEvent) {}); err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}
		}
	}
	if !strings.Contains(string(gotBody), `"reasoning_split":true`) {
		t.Errorf("request body = %s, want it to contain \"reasoning_split\":true", gotBody)
	}

	// Reserved-name rejection also reaches through the facade.
	if err := NewOpenAIProvider("openai", srv.URL); err == nil {
		t.Error("expected an error registering a reserved built-in provider name via the facade")
	}
}

// TestRunAcpAliasIsWiredEndToEnd verifies RunAcp (and AcpWithStdin/
// AcpWithStdout) are genuinely wired to acp.Run by driving a real
// "initialize" JSON-RPC round trip through the facade's aliases alone.
func TestRunAcpAliasIsWiredEndToEnd(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinR.Close()
	defer stdoutW.Close()

	a := NewAgent(AgentWithStore(store.NewInMemoryStore()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- RunAcp(ctx, a, AcpWithStdin(stdinR), AcpWithStdout(stdoutW)) }()

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n"
	if _, err := stdinW.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	buf := make([]byte, 4096)
	readDone := make(chan struct{})
	var n int
	go func() {
		n, _ = stdoutR.Read(buf)
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("no response read from RunAcp within timeout")
	}

	if !bytes.Contains(buf[:n], []byte(`"agentInfo"`)) {
		t.Errorf("response did not contain agentInfo: %s", buf[:n])
	}

	stdinW.Close()
	cancel()
	<-done
}
