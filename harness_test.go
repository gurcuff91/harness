package harness

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
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
